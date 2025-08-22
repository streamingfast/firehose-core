package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"text/template"
	"time"

	"buf.build/gen/go/bufbuild/reflect/connectrpc/go/buf/reflect/v1beta1/reflectv1beta1connect"
	reflectv1beta1 "buf.build/gen/go/bufbuild/reflect/protocolbuffers/go/buf/reflect/v1beta1"
	connect "connectrpc.com/connect"
	"github.com/iancoleman/strcase"
	registry "github.com/pinax-network/graph-networks-libs/packages/golang/lib"
	"github.com/streamingfast/cli"
	networks "github.com/streamingfast/firehose-networks"
	"google.golang.org/protobuf/proto"
)

//go:embed *.gotmpl
var templates embed.FS

func main() {
	cli.Ensure(len(os.Args) == 3 || len(os.Args) == 4, "go run ./generator <output_package> <package_name> [<block-type-filter-regex>]")

	authToken := os.Getenv("BUFBUILD_AUTH_TOKEN")
	cli.Ensure(authToken != "", "You must set the BUFBUILD_AUTH_TOKEN environment variable to generate well known registry. See https://buf.build/docs/bsr/authentication")

	outputPackage := os.Args[1]
	packageName := os.Args[2]
	blockTypeFilterRaw := ".*"
	if len(os.Args) == 4 {
		blockTypeFilterRaw = os.Args[3]
	}

	blockTypeFilter, err := regexp.Compile(blockTypeFilterRaw)
	cli.NoError(err, "Unable to compile filter regex %q", blockTypeFilterRaw)

	client := reflectv1beta1connect.NewFileDescriptorSetServiceClient(
		http.DefaultClient,
		"https://buf.build",
	)

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
		request := connect.NewRequest(&reflectv1beta1.GetFileDescriptorSetRequest{
			Module: wellKnownProtoRepo,
		})

		request.Header().Set("Authorization", "Bearer "+authToken)
		fileDescriptorSet, err := client.GetFileDescriptorSet(context.Background(), request)
		if err != nil {
			log.Fatalf("failed to call file descriptor set service: %v", err)
			return
		}

		for _, file := range fileDescriptorSet.Msg.FileDescriptorSet.File {
			cnt, err := proto.Marshal(file)
			if err != nil {
				log.Fatalf("failed to marshall proto file %s: %v", file.GetName(), err)
				return
			}
			name := ""
			if file.Name != nil {
				name = *file.Name
			}

			if !protoFileExists(name, cnt, protofiles) {
				protofiles = append(protofiles, ProtoFile{
					Name:                  name,
					Data:                  cnt,
					BufRegistryPackageURL: buildBufRegistryPackageURL(wellKnownProtoRepo, derefPtr(file.Package, ""), fileDescriptorSet.Msg.Version),
				})
				log.Printf("Added proto %s", name)
			}
		}
		// avoid hitting the buf.build rate limit
		time.Sleep(1 * time.Second)
	}

	tmpl, err := template.New("wellknown").Funcs(templateFunctions()).ParseFS(templates, "*.gotmpl")
	cli.NoError(err, "Unable to instantiate template")

	if outputPackage == "-" {
		// Output to stdout (single file for compatibility)
		err = tmpl.ExecuteTemplate(os.Stdout, "template.gotmpl", map[string]any{
			"Package":    packageName,
			"ProtoFiles": protofiles,
		})
		cli.NoError(err, "Unable to render template")
	} else {
		// Create output directory
		cli.NoError(os.MkdirAll(outputPackage, os.ModePerm), "Unable to create output directory")

		// Generate registry file
		registryFile := filepath.Join(outputPackage, "registry.go")
		file, err := os.Create(registryFile)
		cli.NoError(err, "Unable to create registry file")

		bufferedOut := bufio.NewWriter(file)
		err = tmpl.ExecuteTemplate(bufferedOut, "registry.gotmpl", map[string]any{
			"Package": packageName,
		})
		cli.NoError(err, "Unable to render registry template")
		bufferedOut.Flush()
		file.Close()

		// Generate individual proto files
		for _, protoFile := range protofiles {
			filename := generateFilename(protoFile.Name)
			filepath := filepath.Join(outputPackage, filename+".go")

			file, err := os.Create(filepath)
			cli.NoError(err, "Unable to create proto file %s", filepath)

			bufferedOut := bufio.NewWriter(file)
			err = tmpl.ExecuteTemplate(bufferedOut, "protofile.gotmpl", map[string]any{
				"Package":               packageName,
				"Name":                  protoFile.Name,
				"Data":                  protoFile.Data,
				"BufRegistryPackageURL": protoFile.BufRegistryPackageURL,
			})
			cli.NoError(err, "Unable to render proto file template")
			bufferedOut.Flush()
			file.Close()

			log.Printf("Generated %s", filepath)
		}
	}

	fmt.Println("Done creating well known registry")
}

func buildBufRegistryPackageURL(module string, fullyQualifiedPackage string, revision string) string {
	// Example full URL is https://buf.build/streamingfast/firehose-near/docs/146e2ae8bd9b49e29b132b8627f29a70:sf.near.type.v1
	return fmt.Sprintf("https://%s/docs/%s:%s", module, revision, fullyQualifiedPackage)
}

func protoFileExists(name string, data []byte, protofiles []ProtoFile) bool {
	for _, pf := range protofiles {
		if pf.Name == name && slices.Equal(pf.Data, data) {
			return true
		}
	}
	return false
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
	// Replace separators with dots: sf/ethereum/type/v2/block.proto -> sf.ethereum.type.v2.block.proto
	return strings.ReplaceAll(name, "/", ".")
}

func derefPtr[T any](in *T, orValue T) T {
	if in == nil {
		return orValue
	}

	return *in
}
