/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import "testing"

func TestDatabaseSpec_EffectiveRebuildStrategy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   RebuildStrategy
		want RebuildStrategy
	}{
		{name: "empty defaults to InPlace", in: "", want: RebuildStrategyInPlace},
		{name: "InPlace", in: RebuildStrategyInPlace, want: RebuildStrategyInPlace},
		{name: "BlueGreen", in: RebuildStrategyBlueGreen, want: RebuildStrategyBlueGreen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DatabaseSpec{RebuildStrategy: tc.in}.EffectiveRebuildStrategy()
			if got != tc.want {
				t.Fatalf("EffectiveRebuildStrategy()=%q want %q", got, tc.want)
			}
		})
	}
}
