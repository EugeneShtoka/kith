package local

import "github.com/EugeneShtoka/kith/internal/domain"

// mx is the prefix of an invented Matrix account's rooms and events (the account part
// has AccountKey's shape); mxRooms is its rooms' owner.
const mx = "matrix:0123456789abcdef/"

var mxRooms = domain.AccountRooms(domain.ProtocolMatrix, "0123456789abcdef")
