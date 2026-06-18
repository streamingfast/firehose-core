package main

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/iancoleman/strcase"
	registry "github.com/pinax-network/graph-networks-libs/packages/golang/lib"
	"github.com/streamingfast/cli"
	networks "github.com/streamingfast/firehose-networks"
	"github.com/streamingfast/logging"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

//go:embed *.gotmpl
var templates embed.FS

var zlog, _ = logging.PackageLogger("generator", "github.com/streamingfast/firehose-core/proto/generator")

func main() {
	logging.InstantiateLoggers()

	cli.Ensure(len(os.Args) >= 3 && len(os.Args) <= 5, "go run ./generator <output_package> <package_name> [<block-type-filter-regex>] [<buf-revision>]")

	// Auth token is optional: public BSR modules are accessible without it, but
	// providing one avoids rate-limiting for large regenerations.
	authToken := os.Getenv("BUFBUILD_AUTH_TOKEN")
	if authToken == "" {
		zlog.Warn("BUFBUILD_AUTH_TOKEN not set — proceeding without authentication (rate limits may apply)")
	}

	outputPackage := os.Args[1]
	packageName := os.Args[2]
	blockTypeFilterRaw := ".*"
	bufRevision := "main"
	if len(os.Args) >= 4 {
		blockTypeFilterRaw = os.Args[3]
	}
	if len(os.Args) >= 5 {
		bufRevision = os.Args[4]
	}

	blockTypeFilter, err := regexp.Compile(blockTypeFilterRaw)
	cli.NoError(err, "Unable to compile filter regex %q", blockTypeFilterRaw)

	// Some networks specify the same Buf module; discard duplicates already fetched.
	seenBufModules := make(map[string]bool)
	// Track file names → marshaled bytes for deduplication across modules.
	// Name-based deduplication is required: a FileDescriptorSet must have unique names.
	// We also keep the bytes so we can warn when the same path appears with different
	// content (version skew between BSR modules).
	seenFiles := make(map[string][]byte)

	var protofiles []ProtoFile
	registeredNetworks := make([]*registry.Network, 0, len(networks.GetFirehoseRegistry()))
	for _, n := range networks.GetFirehoseRegistry() {
		registeredNetworks = append(registeredNetworks, n)
	}

	sort.Slice(registeredNetworks, func(i, j int) bool {
		return registeredNetworks[i].ID < registeredNetworks[j].ID
	})

	for _, protocol := range registeredNetworks {
		if protocol.Firehose.BufURL == "" {
			continue
		}

		if !blockTypeFilter.MatchString(protocol.Firehose.BlockType) {
			continue
		}

		wellKnownProtoRepo := strings.TrimPrefix(protocol.Firehose.BufURL, "https://")
		if seenBufModules[wellKnownProtoRepo] {
			continue
		}
		seenBufModules[wellKnownProtoRepo] = true

		// wellKnownProtoRepo is "buf.build/<owner>/<module>"
		parts := strings.SplitN(wellKnownProtoRepo, "/", 3)
		cli.Ensure(len(parts) == 3, "unexpected BufURL format %q (expected buf.build/owner/module)", wellKnownProtoRepo)
		host, owner, moduleName := parts[0], parts[1], parts[2]

		fds, commitID := fetchFileDescriptorSet(host, owner, moduleName, bufRevision, authToken)

		zlog.Info("resolved commit", zap.String("module", wellKnownProtoRepo), zap.String("commit", commitID))

		for _, file := range fds.File {
			name := derefPtr(file.Name, "")

			cnt, err := proto.Marshal(file)
			cli.NoError(err, "Failed to marshal proto file %s", name)

			if existing, seen := seenFiles[name]; seen {
				if !bytes.Equal(existing, cnt) {
					zlog.Warn("file has different content than a previously seen copy — keeping the first occurrence; check for version skew between BSR modules",
						zap.String("file", name),
						zap.String("module", wellKnownProtoRepo),
					)
				}
				continue
			}
			seenFiles[name] = cnt

			protofiles = append(protofiles, ProtoFile{
				Name:                  name,
				Data:                  cnt,
				BufRegistryPackageURL: buildBufRegistryPackageURL(wellKnownProtoRepo, derefPtr(file.Package, ""), commitID),
			})
			zlog.Info("added proto", zap.String("file", name))
		}

		// Avoid hitting the BSR rate limit between module fetches.
		time.Sleep(1 * time.Second)
	}

	tmpl, err := template.New("wellknown").Funcs(templateFunctions()).ParseFS(templates, "*.gotmpl")
	cli.NoError(err, "Unable to instantiate template")

	if outputPackage == "-" {
		err = tmpl.ExecuteTemplate(os.Stdout, "template.gotmpl", map[string]any{
			"Package":    packageName,
			"ProtoFiles": protofiles,
		})
		cli.NoError(err, "Unable to render template")
	} else {
		cli.NoError(os.MkdirAll(outputPackage, os.ModePerm), "Unable to create output directory")

		registryFile := filepath.Join(outputPackage, "registry.go")
		f, err := os.Create(registryFile)
		cli.NoError(err, "Unable to create registry file")

		w := bufio.NewWriter(f)
		cli.NoError(tmpl.ExecuteTemplate(w, "registry.gotmpl", map[string]any{"Package": packageName}), "Unable to render registry template")
		cli.NoError(w.Flush(), "Unable to flush registry file")
		f.Close()

		for _, protoFile := range protofiles {
			outPath := filepath.Join(outputPackage, generateFilename(protoFile.Name)+".go")

			f, err := os.Create(outPath)
			cli.NoError(err, "Unable to create proto file %s", outPath)

			w := bufio.NewWriter(f)
			cli.NoError(tmpl.ExecuteTemplate(w, "protofile.gotmpl", map[string]any{
				"Package":               packageName,
				"Name":                  protoFile.Name,
				"Data":                  protoFile.Data,
				"BufRegistryPackageURL": protoFile.BufRegistryPackageURL,
			}), "Unable to render proto file template")
			cli.NoError(w.Flush(), "Unable to flush proto file %s", outPath)
			f.Close()

			zlog.Info("generated", zap.String("file", outPath))
		}
	}

	zlog.Info("done creating well known registry")
}

// fetchFileDescriptorSet calls the BSR Connect API to fetch a module's FileDescriptorSet
// with source_code_info, and returns the resolved commit ID alongside it.
func fetchFileDescriptorSet(host, owner, moduleName, revision, authToken string) (*descriptorpb.FileDescriptorSet, string) {
	type reqName struct {
		Owner  string `json:"owner"`
		Module string `json:"module"`
		Label  string `json:"label"`
	}
	type reqResourceRef struct {
		Name reqName `json:"name"`
	}
	type connectReq struct {
		ResourceRef           reqResourceRef `json:"resourceRef"`
		IncludeSourceCodeInfo bool           `json:"includeSourceCodeInfo"`
	}

	reqBody, err := json.Marshal(connectReq{
		ResourceRef:           reqResourceRef{Name: reqName{Owner: owner, Module: moduleName, Label: revision}},
		IncludeSourceCodeInfo: true,
	})
	cli.NoError(err, "Failed to marshal Connect request for %s/%s", owner, moduleName)

	connectURL := fmt.Sprintf("https://%s/buf.registry.module.v1.FileDescriptorSetService/GetFileDescriptorSet", host)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, connectURL, bytes.NewReader(reqBody))
	cli.NoError(err, "Failed to build request for %s/%s", owner, moduleName)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	resp, err := http.DefaultClient.Do(req)
	cli.NoError(err, "HTTP request failed for %s/%s", owner, moduleName)

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	cli.NoError(err, "Failed to read response body for %s/%s", owner, moduleName)
	cli.Ensure(resp.StatusCode == http.StatusOK, "BSR request for %s/%s returned HTTP %d: %s", owner, moduleName, resp.StatusCode, string(body))

	var connectResp struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
		FileDescriptorSet json.RawMessage `json:"fileDescriptorSet"`
	}
	cli.NoError(json.Unmarshal(body, &connectResp), "Failed to parse Connect response for %s/%s", owner, moduleName)

	fds := &descriptorpb.FileDescriptorSet{}
	cli.NoError(protojson.Unmarshal(connectResp.FileDescriptorSet, fds), "Failed to unmarshal FileDescriptorSet for %s/%s", owner, moduleName)

	return fds, connectResp.Commit.ID
}

func buildBufRegistryPackageURL(module string, fullyQualifiedPackage string, commitID string) string {
	// Example: https://buf.build/streamingfast/firehose-near/docs/d39e7444b0ba4b7685a06b6cdf65397b:sf.near.type.v1
	return fmt.Sprintf("https://%s/docs/%s:%s", module, commitID, fullyQualifiedPackage)
}

type ProtoFile struct {
	Name                  string
	Data                  []byte
	BufRegistryPackageURL string
	BytesEncoding         string
}

func templateFunctions() template.FuncMap {
	return template.FuncMap{
		"lower":      strings.ToLower,
		"pascalCase": strcase.ToCamel,
		"camelCase":  strcase.ToLowerCamel,
		"toHex": func(in []byte) string {
			return hex.EncodeToString(in)
		},
		"toBase64": func(in []byte) string {
			return base64.StdEncoding.EncodeToString(in)
		},
	}
}

func generateFilename(name string) string {
	// sf/ethereum/type/v2/block.proto -> sf.ethereum.type.v2.block.proto
	return strings.ReplaceAll(name, "/", ".")
}

func derefPtr[T any](in *T, orValue T) T {
	if in == nil {
		return orValue
	}
	return *in
}
