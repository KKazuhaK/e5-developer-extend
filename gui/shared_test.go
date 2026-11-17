package main

import "testing"

func TestSaveDialogOptions(t *testing.T) {
	o := saveDialogOptions("Save the backup file", "e5extend-backup-20261116.e5backup")
	if o.Title != "Save the backup file" || o.Message != o.Title {
		t.Errorf("title %q, message %q", o.Title, o.Message)
	}
	if o.Filename != "e5extend-backup-20261116.e5backup" {
		t.Errorf("filename %q", o.Filename)
	}
	if len(o.Filters) != 1 || o.Filters[0].Pattern != "*.e5backup" {
		t.Errorf("filters %+v", o.Filters)
	}
}
