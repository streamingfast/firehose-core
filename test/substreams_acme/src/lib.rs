mod pb;

use num_traits::ToPrimitive;
use pb::testdata::v1 as testdata;
use pb::sf::acme::r#type::v1::Block;

use substreams::Hex;


substreams_ethereum::init!();

#[substreams::handlers::map]
fn map_test_data(blk: Block) -> testdata::TestData {
    let mut test_data = testdata::TestData::default();
    let header = blk.header.clone().unwrap();
    test_data.block_hash = Hex(header.hash).to_string();
    test_data.block_number = header.height.to_u64().unwrap();
    test_data
}
