// @generated
#[allow(clippy::derive_partial_eq_without_eq)]
#[derive(Clone, PartialEq, ::prost::Message)]
pub struct TestData {
    #[prost(string, tag="1")]
    pub block_hash: ::prost::alloc::string::String,
    #[prost(uint64, tag="2")]
    pub block_number: u64,
    #[prost(message, optional, tag="3")]
    pub block_timestamp: ::core::option::Option<::prost_types::Timestamp>,
    #[prost(uint64, tag="4")]
    pub transactions_len: u64,
}
// @@protoc_insertion_point(module)
