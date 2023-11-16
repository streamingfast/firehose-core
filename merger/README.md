# StreamingFast Merger

[![reference](https://img.shields.io/badge/godoc-reference-5272B4.svg?style=flat-square)](https://pkg.go.dev/github.com/streamingfast/firehose-core/firehose/merger)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

The merger process is responsible for accumulating blocks from all
forks visible by the pool of instrumented nodes, and builds the famous
100-blocks files consumed by `bstream`'s _FileSource_ and may other
StreamingFast processes.

## Design

The Merger section of the official Firehose documentation provides additional information on its design details.

https://firehose.streamingfast.io/concepts-and-architeceture/components#merger

## Contributing

**Issues and PR in this repo related strictly to the merger functionalities**

Report any protocol-specific issues in their
[respective repositories](https://github.com/streamingfast/streamingfast#protocols)

**Please first refer to the general
[streamingfast contribution guide](https://github.com/streamingfast/streamingfast/blob/master/CONTRIBUTING.md)**,
if you wish to contribute to this code base.


## License

[Apache 2.0](LICENSE)
