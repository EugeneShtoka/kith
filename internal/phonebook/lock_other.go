//go:build !unix

package phonebook

// lockDir holds nothing where the folder cannot be locked as unix locks it: kith's
// writers write as every other program does, each over the file as it read it.
func lockDir(string) func() { return func() {} }
