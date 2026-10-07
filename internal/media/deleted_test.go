package media

import (
	"os"
	"testing"
)

// A deleted message's attachment moves to deleted/, its drawings go, and trimming the
// cache leaves it alone; one fetched after the deletion is kept there directly.
func TestADeletedAttachmentIsSetAside(t *testing.T) {
	t.Parallel()
	c, err := New(t.TempDir(), 1) // a one-byte cache: Trim evicts everything it reads
	if err != nil {
		t.Fatal(err)
	}
	path, err := c.Write("$gone", "cat.jpg", "image/jpeg", []byte("picture"))
	if err != nil {
		t.Fatal(err)
	}
	if perr := c.PutRender("$gone", 10, 5, []byte("drawn")); err != nil {
		t.Fatal(perr)
	}
	aside, err := c.SetAside("$gone", "cat.jpg", "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the original is still where pictures are viewed from: %v", err)
	}
	if _, ok := c.Render("$gone", 10, 5); ok {
		t.Error("the deleted picture's drawing was kept")
	}
	if _, err := c.Trim(); err != nil {
		t.Fatal(err)
	}
	if data, ok := c.ReadAside("$gone", "cat.jpg", "image/jpeg"); !ok || string(data) != "picture" || aside != c.AsidePath("$gone", "cat.jpg", "image/jpeg") {
		t.Errorf("kept aside = %q, %v; want it through trimming", data, ok)
	}
	if _, err := c.SetAside("$never", "x.png", "image/png"); err != nil {
		t.Errorf("setting aside what the cache never held = %v, want nothing to do", err)
	}
	if _, err := c.WriteAside("$late", "late.png", "image/png", []byte("fetched later")); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.ReadAside("$late", "late.png", "image/png"); !ok {
		t.Error("an attachment fetched after its deletion was not kept aside")
	}
}
