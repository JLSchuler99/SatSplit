package lightning

import (
	"encoding/hex"
	"errors"
	"strings"
)

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// PaymentHash returns the hex payment hash of a BOLT11 invoice. The hash is
// what a wallet lists next to a received payment, so it lets a host match an
// invoice to a payment by hand. The checksum and signature are not checked;
// the invoice is only read, never trusted for payment here.
func PaymentHash(invoice string) (string, error) {
	inv := strings.ToLower(strings.TrimSpace(invoice))
	inv = strings.TrimPrefix(inv, "lightning:")
	sep := strings.LastIndexByte(inv, '1')
	if sep < 2 {
		return "", errors.New("bolt11: no separator")
	}
	body := inv[sep+1:]
	// 7 groups of timestamp, 104 of signature, 6 of checksum.
	if len(body) < 7+104+6 {
		return "", errors.New("bolt11: too short")
	}
	data := make([]byte, 0, len(body)-6)
	for i := 0; i < len(body)-6; i++ {
		v := strings.IndexByte(bech32Charset, body[i])
		if v < 0 {
			return "", errors.New("bolt11: invalid character")
		}
		data = append(data, byte(v))
	}
	end := len(data) - 104
	for pos := 7; pos+3 <= end; {
		tag := data[pos]
		length := int(data[pos+1])<<5 | int(data[pos+2])
		pos += 3
		if pos+length > end {
			return "", errors.New("bolt11: field overruns invoice")
		}
		// Tag 1 ("p") with 52 groups is the 256-bit payment hash.
		if tag == 1 && length == 52 {
			return hex.EncodeToString(groupsToBytes(data[pos:pos+length], 32)), nil
		}
		pos += length
	}
	return "", errors.New("bolt11: no payment hash")
}

// groupsToBytes packs 5-bit groups into n bytes and drops the padding bits.
func groupsToBytes(groups []byte, n int) []byte {
	out := make([]byte, 0, n)
	var acc uint32
	var bits uint
	for _, g := range groups {
		acc = acc<<5 | uint32(g)
		bits += 5
		for bits >= 8 && len(out) < n {
			bits -= 8
			out = append(out, byte(acc>>bits))
		}
	}
	return out
}
