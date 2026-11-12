package browser

import "testing"

func TestSnapshotParentCarriesIgnoredAncestorsAcrossBreadthFirstNodes(t *testing.T) {
	ancestors := map[string]string{}
	for _, tc := range []struct {
		id, parent, emitted, want string
		known                     bool
	}{
		{"root", "", "@root", "", true},
		{"ignored", "root", "", "@root", true},
		{"table", "root", "@table", "@root", true},
		{"tab", "ignored", "@tab", "@root", true},
		{"row", "table", "@row", "@table", true},
		{"cell1", "row", "@cell1", "@row", true},
		{"cell2", "row", "@cell2", "@row", true},
		{"check", "cell1", "@check", "@cell1", true},
		{"label", "cell2", "@label", "@cell2", true},
		{"orphan", "missing", "@orphan", "", false},
	} {
		got, known := snapshotParent(ancestors, tc.id, tc.parent)
		if got != tc.want || known != tc.known {
			t.Errorf("%s: got parent %q known %v, want %q %v", tc.id, got, known, tc.want, tc.known)
		}
		if tc.emitted != "" {
			ancestors[tc.id] = tc.emitted
		}
	}
}
