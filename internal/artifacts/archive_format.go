package artifacts

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Python tarfile uses a particular PAX header name, field order, timestamp
// representation and 20-block record padding. A generic tar.Writer produces
// readable archives but different bytes, breaking immutable publication retries.
type archiveHeader struct {
	name, link, uname, gname string
	mode, uid, gid, size     int64
	mtime                    float64
	kind                     byte
}

func pythonFloat(value float64) string {
	format := byte('f')
	if value != 0 && (math.Abs(value) < 1e-4 || math.Abs(value) >= 1e16) {
		format = 'g'
	}
	text := strconv.FormatFloat(value, format, -1, 64)
	if !strings.ContainsAny(text, ".e") {
		text += ".0"
	}
	return text
}

func paxRecord(key, value string) []byte {
	body := " " + key + "=" + value + "\n"
	size := len(body) + 1
	for {
		next := len(body) + len(strconv.Itoa(size))
		if next == size {
			return []byte(strconv.Itoa(size) + body)
		}
		size = next
	}
}

func (a *archiveStream) header(h archiveHeader) error {
	var pax []byte
	binary := false
	for _, field := range []struct {
		key, value string
		limit      int
	}{{"path", h.name, 100}, {"linkpath", h.link, 100}, {"uname", h.uname, 32}, {"gname", h.gname, 32}} {
		extended := len(field.value) > field.limit
		for _, r := range field.value {
			if r > 127 {
				extended = true
			}
		}
		if extended {
			pax = append(pax, paxRecord(field.key, field.value)...)
			binary = binary || !utf8.ValidString(field.value)
		}
	}
	for _, field := range []struct {
		key   string
		value *int64
		limit int64
	}{{"uid", &h.uid, 1 << 21}, {"gid", &h.gid, 1 << 21}, {"size", &h.size, 1 << 33}} {
		if *field.value < 0 || *field.value >= field.limit {
			pax = append(pax, paxRecord(field.key, strconv.FormatInt(*field.value, 10))...)
			*field.value = 0
		}
	}
	pax = append(pax, paxRecord("mtime", pythonFloat(h.mtime))...)
	if binary {
		pax = append([]byte("21 hdrcharset=BINARY\n"), pax...)
	}
	if _, err := a.Write(tarBlock(archiveHeader{name: "././@PaxHeader", kind: 'x', size: int64(len(pax))})); err != nil {
		return err
	}
	if _, err := a.Write(pax); err != nil {
		return err
	}
	if err := a.zeros((512 - int64(len(pax))%512) % 512); err != nil {
		return err
	}
	h.mtime = math.RoundToEven(h.mtime)
	if h.mtime < 0 || h.mtime >= 1<<33 {
		h.mtime = 0
	}
	_, err := a.Write(tarBlock(h))
	return err
}

func tarBlock(h archiveHeader) []byte {
	block := make([]byte, 512)
	text := func(offset, width int, value string) {
		var ascii []byte
		for _, r := range value {
			if r > 127 {
				ascii = append(ascii, '?')
			} else {
				ascii = append(ascii, byte(r))
			}
		}
		copy(block[offset:offset+width], ascii)
	}
	number := func(offset, width int, value int64) { text(offset, width, fmt.Sprintf("%0*o", width-1, value)) }
	text(0, 100, h.name)
	number(100, 8, h.mode)
	number(108, 8, h.uid)
	number(116, 8, h.gid)
	number(124, 12, h.size)
	number(136, 12, int64(h.mtime))
	copy(block[148:156], bytes.Repeat([]byte{' '}, 8))
	block[156] = h.kind
	text(157, 100, h.link)
	copy(block[257:265], []byte("ustar\x0000"))
	text(265, 32, h.uname)
	text(297, 32, h.gname)
	checksum := 0
	for _, b := range block {
		checksum += int(b)
	}
	copy(block[148:155], []byte(fmt.Sprintf("%06o\x00", checksum)))
	return block
}
