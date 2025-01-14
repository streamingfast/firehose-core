// Copyright 2021 dfuse Platform Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package print

import "testing"

func Test_doesLookLikeStoreURLFile(t *testing.T) {
	type args struct {
		path string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "just block number",
			args: args{"gs://bucket/path/to/file/0000000001"},
			want: true,
		},
		{
			name: "block number + dbin",
			args: args{"gs://bucket/path/to/file/0000000001.dbin"},
			want: true,
		},
		{
			name: "block number + dbin +zst",
			args: args{"gs://bucket/path/to/file/0000000001.dbin.zst"},
			want: true,
		},
		{
			name: "wrong block prefix, alone",
			args: args{"gs://bucket/path/to/file/v2"},
			want: false,
		},
		{
			name: "wrong block prefix + dbing",
			args: args{"gs://bucket/path/to/file/v2.dbin"},
			want: false,
		},
		{
			name: "wrong block prefix + dbin + zst",
			args: args{"gs://bucket/path/to/file/v2.dbin.zst"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeStoreURLFile(tt.args.path); got != tt.want {
				t.Errorf("doesLookLikeStoreURLFile() = %v, want %v", got, tt.want)
			}
		})
	}
}
