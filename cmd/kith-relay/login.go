package main

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"rsc.io/qr"

	backendv1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/config"
)

// A login the window leads is kithd's (BackendService's login calls, forwarded); the
// relay does the two things for it that need Go: writing a new account into the config
// as the TUI's login writes it, and encoding a QR step's data as the TUI encodes it.

// accountTries is how many times a new account is written, each try lost to another
// writer of the config, before the relay gives up.
const accountTries = 4

// WriteAccount writes the record into the bridge's config through kithd: read, the
// record added (config.Config.Write, as the TUI's login does), written on the revision
// read. Another writer between the read and the write (ABORTED) has the relay read it
// again and add the record to that, so neither change is lost.
func (d device) WriteAccount(ctx context.Context, req *connect.Request[devicev1.WriteAccountRequest]) (*connect.Response[devicev1.WriteAccountResponse], error) {
	backend := d.kithdBackend()
	var err error
	for range accountTries {
		var got *connect.Response[backendv1.GetConfigResponse]
		got, err = backend.GetConfig(ctx, connect.NewRequest(&backendv1.GetConfigRequest{}))
		if err != nil {
			return nil, d.at.failure(err)
		}
		cfg, derr := config.Decode(got.Msg.GetConfig())
		if derr != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s: %w", d.configName(), derr))
		}
		if werr := cfg.Write(req.Msg.GetTable(), req.Msg.GetValues()); werr != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("set the account up: %w", werr))
		}
		_, err = backend.UpdateConfig(ctx, connect.NewRequest(&backendv1.UpdateConfigRequest{BaseRevision: got.Msg.GetRevision(), Config: config.Encode(cfg)}))
		if connect.CodeOf(err) != connect.CodeAborted {
			break
		}
	}
	if err != nil {
		return nil, d.at.failure(err)
	}
	return connect.NewResponse(&devicev1.WriteAccountResponse{}), nil
}

// errNoQRData answers a QR code asked for nothing.
var errNoQRData = errors.New("a QR code of nothing")

// QRCode is data's modules, encoded as qrtext encodes a QR step for the TUI.
func (device) QRCode(_ context.Context, req *connect.Request[devicev1.QRCodeRequest]) (*connect.Response[devicev1.QRCodeResponse], error) {
	data := req.Msg.GetData()
	if data == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errNoQRData)
	}
	code, err := qr.Encode(data, qr.L)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("encode a QR code: %w", err))
	}
	dark := make([]byte, code.Size*code.Size)
	for y := range code.Size {
		for x := range code.Size {
			if code.Black(x, y) {
				dark[y*code.Size+x] = 1
			}
		}
	}
	return connect.NewResponse(&devicev1.QRCodeResponse{Dark: dark}), nil
}
