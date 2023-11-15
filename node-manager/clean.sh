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

main() {
  current_dir="`pwd`"
  trap "cd \"$current_dir\"" EXIT
  pushd "$ROOT" &> /dev/null

  cmd=$1; shift

  if [[ $cmd == "" ]]; then
    usage_error "argument <cmd> is required"
  fi

  if [[ ! -d "./cmd/$cmd" ]]; then
    usage_error "argument <cmd> is invalid, valid ones are: \"`ls ./cmd | xargs | tr ' ' ','`\""
  fi

  backup_dir="$ROOT/data/$cmd/backups"
  config_dir="$ROOT/data/$cmd/config"
  data_dir="$ROOT/data/$cmd/storage"
  snapshot_dir="$ROOT/data/$cmd/snapshots"
  mindreader_dir="$ROOT/data/$cmd/deep-mind"

  echo "Cleaning $cmd..."
  if [[ $cmd == "geth_mindreader" ]]; then
    rm -rf $mindreader_dir
    rm -rf $data_dir/geth/chaindata
  elif [[ $cmd == "nodeos_mindreader" || $cmd == "nodeos_manager" ]]; then
    rm -rf $snapshot_dir
    rm -rf $data_dir
  fi

  echo "Done"
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
  echo "usage: clean.sh <cmd> [<cmd arguments>]"
  echo ""
  echo "Clean data of the appropriate manager/mindreader operator"
  echo ""
  echo "Valid <cmd>"
  ls "$ROOT/cmd" | xargs | tr " " "\n" | sed 's/^/ * /'
  echo ""
}

main $@