### Protobuf Registry

The well-known Protobuf definitions are pulled from Buf Build Registry. This makes `firehose-core` able to decode some well-known block files directly.

#### Re-generate

To re-generate the well-known types, simply do:

```bash
go generate registry.go
```

Before re-generating, ensure you have push to Buf Registry the latest version of the definitions.

#### Add new well-known types

Push your definitions to the Buf Registry. Edit file [./generator/generator.go](./generator/generator.go) and add the Buf Registry path of the package in `wellKnownProtoRepos` variables.

Then [re-generate Protobuf definitions](#re-generate) and send a PR with the changes.
