# StreamingFast Relayer

[![reference](https://img.shields.io/badge/godoc-reference-5272B4.svg?style=flat-square)](https://pkg.go.dev/github.com/streamingfast/firehose-core/firehose/relayer)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

The relayer process fans out and propagates blocks from instrumented
blockchain nodes, down to services, serving as a redundant and
highly-available access to streaming block & transaction data.
It is part of **[StreamingFast](https://github.com/streamingfast/streamingfast)**.

Design
The Relayer section of the official Firehose documentation provides additional information on its design details.

https://firehose.streamingfast.io/concepts-and-architeceture/components#relayer

Current implementations:

* [**EOSIO on StreamingFast**](https://github.com/streamingfast/sf-eosio)
* [**Ethereum on StreamingFast**](https://github.com/streamingfast/sf-ethereum)


## Schema

```
			Graph:

		                       [--------------]   [-------------------]
		                       [ Mindreader-1 ]   [ Mindreader-2, ... ]
		                       [--------------]   [-------------------]
		                            \                 /
		                             \               /
		    [-----------------]    [-------------------]
		    [ OneBlocksSource ]    [ MultiplexedSource ]
		    [-----------------]    [-------------------]
		                   \        /
					    [-------------]
					    [ ForkableHub ] (all blocks triggering StepNew)
					    [-------------]
                                 |
                       (hub's single subscriber)
				                 |
 		       [-----------------------------------]
 		       [   pipe Handler: Server.PushBlock  ]
 		       [-----------------------------------]
	                          /          \
		       [-----------------]   [---------------]
		       [ Buffer (dedupe) ]-->[ Subscriptions ]
		       [-----------------]   [---------------]

```

## Contributing

**Issues and PR in this repo related strictly to the relayer functionalities**

Report any protocol-specific issues in their
[respective repositories](https://github.com/streamingfast/streamingfast#protocols)

**Please first refer to the general
[StreamingFast contribution guide](https://github.com/streamingfast/streamingfast/blob/master/CONTRIBUTING.md)**,
if you wish to contribute to this code base.


## License

[Apache 2.0](LICENSE)
