#!/usr/bin/env bash
# Copyright 2019 dfuse Platform Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


ROOT="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

clean="false"

main() {
  current_dir="`pwd`"
  trap "cd \"$current_dir\"" EXIT
  pushd "$ROOT" &> /dev/null

  while getopts "hc" opt; do
    case "$opt" in
        h) usage && exit 0;;
        c) clean="true";;
        \?) usage_error "Invalid option: -$OPTARG";;
    esac
  done
  shift $((OPTIND-1))

  platform=$1; shift
  network=$1; shift

  if [[ $platform != "eos" ]]; then
    usage_error "argument <platform> must be 'eos'"
  fi

  if [[ $clean == "true" ]]; then
    ./clean.sh nodeos_manager
    ./clean.sh nodeos_mindreader
  fi

  case "$platform" in
  eos) eos $platform $network $force;;
  esac
}

eos() {
  platform=$1; shift
  network=$1; shift
  force=$1; shift

  if [[ $network != "mainnet" && $network != "jungle" && $network != "bp" ]]; then
    usage_error "argument <network> must be 'mainnet', 'jungle' or 'bp'"
  fi

  content_dir="$ROOT/boot/${platform}_${network}"

  config_file="$content_dir/config.ini"
  genesis_file="$content_dir/genesis.json"

  manager_config_dir="$ROOT/data/nodeos_manager/config"
  mindreader_config_dir="$ROOT/data/nodeos_mindreader/config"

  mkdir -p "$manager_config_dir" &> /dev/null
  mkdir -p "$mindreader_config_dir" &> /dev/null

  [[ ! -f "$manager_config_dir/config.ini" || clean="true" ]] && cp "$config_file" "$manager_config_dir/config.ini"
  [[ ! -f "$manager_config_dir/genesis.json" || clean="true" ]] && cp "$genesis_file" "$manager_config_dir/genesis.json"

  [[ ! -f "$mindreader_config_dir/config.ini" || clean="true" ]] && cp "$config_file" "$mindreader_config_dir/config.ini"
  [[ ! -f "$mindreader_config_dir/genesis.json" || clean="true" ]] && cp "$genesis_file" "$mindreader_config_dir/genesis.json"
}

usage_error() {
  message="$1"
  exit_code="$2"

  echo "ERROR: $message"
  echo ""
  usage
  exit ${exit_code:-1}
}

usage() {
  echo "usage: boot.sh [-c] <platform> <network>"
  echo ""
  echo "Create the necessary files for chain bootstrap (for example, the 'genesis.json' file"
  echo "for EOS Mainnet syncing)."
  echo ""
  echo "Flags"
  echo ""
  echo "    -c         Clean all files before copying the 'config.ini' and 'genesis.json'"
}

main $@