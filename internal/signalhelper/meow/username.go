//go:build signal

package meow

// libsignal's username crypto, which signalmeow does not wrap. The functions are in
// libsignal_ffi, which signalmeow's libsignalgo links; their prototypes are copied from
// its libsignal-ffi.h.

/*
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>

typedef struct SignalFfiError SignalFfiError;
typedef struct {
  const uint8_t* base;
  size_t length;
} SignalBorrowedBuffer;

SignalFfiError* signal_username_link_decrypt_username(const int8_t** out,
    SignalBorrowedBuffer entropy, SignalBorrowedBuffer encrypted_username);
SignalFfiError* signal_username_hash(uint8_t (*out)[32], const int8_t* username);
SignalFfiError* signal_error_get_message(const int8_t** out, const SignalFfiError* err);
void signal_error_free(SignalFfiError* err);
void signal_free_string(const int8_t* buf);
*/
import "C"

import (
	"errors"
	"runtime"
	"unsafe"
)

// decryptUsername is the username a username link's key opens from the ciphertext
// Signal keeps for its handle.
func decryptUsername(key, ciphertext []byte) (string, error) {
	var pinner runtime.Pinner
	defer pinner.Unpin()
	var out *C.int8_t
	failed := C.signal_username_link_decrypt_username(&out, borrowed(&pinner, key), borrowed(&pinner, ciphertext)) //nolint:gocritic // it reads cgo's rewrite of the C call, not this line
	if err := ffiError(failed); err != nil {                                                                       //nolint:gocritic // it reads cgo's rewrite of the C call, not this line
		return "", err
	}
	defer C.signal_free_string(out)
	return C.GoString((*C.char)(unsafe.Pointer(out))), nil
}

// usernameHash is the hash Signal looks a username up by.
func usernameHash(username string) ([]byte, error) {
	name := C.CString(username)
	defer C.free(unsafe.Pointer(name))
	var out [32]C.uint8_t
	failed := C.signal_username_hash(&out, (*C.int8_t)(unsafe.Pointer(name)))
	if err := ffiError(failed); err != nil { //nolint:gocritic // it reads cgo's rewrite of the C call, not this line
		return nil, err
	}
	return C.GoBytes(unsafe.Pointer(&out), C.int(len(out))), nil //nolint:gocritic // it reads cgo's rewrite of the C call, not this line
}

// borrowed lends b to one call, pinned so Go keeps it in place meanwhile.
func borrowed(pinner *runtime.Pinner, b []byte) C.SignalBorrowedBuffer {
	if len(b) == 0 {
		return C.SignalBorrowedBuffer{}
	}
	pinner.Pin(&b[0])
	return C.SignalBorrowedBuffer{base: (*C.uint8_t)(unsafe.Pointer(&b[0])), length: C.size_t(len(b))}
}

// ffiError is libsignal's error as a Go one, freed.
func ffiError(e *C.SignalFfiError) error {
	if e == nil {
		return nil
	}
	defer C.signal_error_free(e)
	var msg *C.int8_t
	failed := C.signal_error_get_message(&msg, e) //nolint:gocritic // it reads cgo's rewrite of the C call, not this line
	if failed != nil || msg == nil {
		if failed != nil {
			C.signal_error_free(failed)
		}
		return errors.New("signal: libsignal failed without saying why")
	}
	defer C.signal_free_string(msg)
	return errors.New("signal: " + C.GoString((*C.char)(unsafe.Pointer(msg))))
}
