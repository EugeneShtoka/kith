package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gioui.org/widget"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// save is a file message's Save button and what saving it came to.
type save struct {
	btn  widget.Clickable
	busy bool
	said string // "Saved to …" or why not; "" before a save
}

// layoutSave is a file's "Save name (size)" button; saving puts it in ~/Downloads.
func (u *ui) layoutSave(ctx context.Context, gtx C, m domain.Message) D {
	s := u.saves[m.ID]
	if s == nil {
		s = new(save)
		u.saves[m.ID] = s
	}
	if s.btn.Clicked(gtx) && !s.busy {
		s.busy, s.said = true, "Saving"
		go u.saveFile(ctx, m, s)
	}
	label := "Save " + m.Media.Name
	if m.Media.Size > 0 {
		label += fmt.Sprintf(" (%s)", size(m.Media.Size))
	}
	if s.said != "" {
		label = s.said
	}
	return u.button(gtx, &s.btn, label, !s.busy, false)
}

func (u *ui) saveFile(ctx context.Context, m domain.Message, s *save) {
	said := ""
	data, err := u.backend.LoadImage(ctx, m.RoomID, m.ID)
	if err == nil {
		var path string
		path, err = freeName(m.Media.Name)
		if err == nil {
			err = writeNew(path, data)
			said = "Saved to " + path
		}
	}
	if err != nil {
		said = "Not saved: " + err.Error()
	}
	u.mu.Lock()
	s.busy, s.said = false, said
	u.mu.Unlock()
	u.w.Invalidate()
}

// writeNew writes data to a file that must not exist yet, so a save never replaces one.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// freeName is name in ~/Downloads, numbered past any file already there.
func freeName(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name = filepath.Base(name) // a sender's name never leaves the folder
	if name == "." || name == "/" || name == "" {
		name = "file"
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; ; i++ {
		try := name
		if i > 0 {
			try = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		path := filepath.Join(dir, try)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return path, nil
		}
	}
}

// size is n bytes as the desktop writes it: "38 KB".
func size(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}
