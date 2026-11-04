// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

// Package edl discovers serial-addressable EDL devices without opening USB interfaces.
package edl

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arduino/arduino-flasher-cli/internal/types/serial"
)

type Device struct {
	// Serial includes 0x so copying digit-only serials into the CLI is unambiguous.
	Serial  string `json:"serial"`
	Product string `json:"product"`
	Path    string `json:"path"`
}
type usbDevice struct {
	Vendor, ProductID uint64
	Product, Path     string
}

var productSerial = regexp.MustCompile(`_SN:([0-9A-Fa-f]+)(?:[ _]|$)`)

// Discover returns a sorted snapshot. It never reboots or claims a board, and does
// not infer a board model from the Qualcomm product descriptor.
func Discover(ctx context.Context) ([]Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var candidates []usbDevice
	var err error
	switch runtime.GOOS {
	case "linux":
		candidates, err = linuxDevices(ctx, "/sys/bus/usb/devices")
	case "darwin":
		var data []byte
		data, err = exec.CommandContext(ctx, "/usr/sbin/ioreg", "-a", "-r", "-c", "IOUSBHostDevice").Output()
		if err == nil {
			candidates, err = darwinDevices(data)
		}
	case "windows":
		// BusReportedDeviceDesc is the USB product descriptor QDL matches. Friendly
		// names (often a COM port label) and PnP instance suffixes are not EDL serials.
		const script = `$ErrorActionPreference='Stop'; $items=@(Get-PnpDevice -PresentOnly | Where-Object { $_.InstanceId -match '^USB\\VID_05C6&PID_9008' -and $_.InstanceId -notmatch '&MI_[0-9A-F]{2}' } | ForEach-Object { $d=Get-PnpDeviceProperty -InstanceId $_.InstanceId -KeyName 'DEVPKEY_Device_BusReportedDeviceDesc'; [pscustomobject]@{ Product=[string]$d.Data; Path=$_.InstanceId } }); ConvertTo-Json -InputObject $items -Compress`
		var data []byte
		data, err = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Output()
		if err == nil {
			candidates, err = windowsDevices(data)
		}
	default:
		return nil, fmt.Errorf("EDL discovery is unsupported on %s; use --serial", runtime.GOOS)
	}
	if err != nil {
		return nil, fmt.Errorf("EDL discovery: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return addressable(candidates)
}

func addressable(candidates []usbDevice) ([]Device, error) {
	out := make([]Device, 0)
	paths := map[string]usbDevice{}
	serials := map[string]string{}
	for _, d := range candidates {
		// 900e is a crash-dump mode; do not select it for automatic flashing.
		if d.Vendor != 0x05c6 || d.ProductID != 0x9008 {
			continue
		}
		if d.Path == "" || d.Path == "ioreg:" {
			return nil, fmt.Errorf("EDL device has no physical path")
		}
		if previous, ok := paths[d.Path]; ok {
			if previous != d {
				return nil, fmt.Errorf("USB device changed during discovery at %s", d.Path)
			}
			continue
		}
		paths[d.Path] = d
		match := productSerial.FindStringSubmatch(d.Product)
		if len(match) != 2 {
			return nil, fmt.Errorf("EDL device at %s has no QDL-addressable serial in its product descriptor", d.Path)
		}
		s, err := serial.FromHex(match[1])
		// Bundled QDL matches the product's serial literally, including case and width.
		if err != nil || s.Hex() != match[1] {
			return nil, fmt.Errorf("unsupported EDL serial %q at %s", match[1], d.Path)
		}
		if previous, ok := serials[s.Hex()]; ok {
			return nil, fmt.Errorf("ambiguous EDL serial 0x%s at %s and %s; disconnect one device", s.Hex(), previous, d.Path)
		}
		serials[s.Hex()] = d.Path
		out = append(out, Device{Serial: "0x" + s.Hex(), Product: d.Product, Path: d.Path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Serial < out[j].Serial })
	return out, nil
}

func linuxDevices(ctx context.Context, root string) ([]usbDevice, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []usbDevice
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dir := filepath.Join(root, entry.Name())
		read := func(name string) (string, error) {
			b, err := os.ReadFile(filepath.Join(dir, name))
			return strings.TrimSpace(string(b)), err
		}
		vendor, err := read("idVendor")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(vendor, "05c6") {
			continue
		}
		pid, err := read("idProduct")
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(pid, "9008") {
			continue
		}
		product, err := read("product")
		if err != nil {
			return nil, err
		}
		out = append(out, usbDevice{Vendor: 0x05c6, ProductID: 0x9008, Product: product, Path: dir})
	}
	return out, nil
}

type plistNode struct {
	XMLName  xml.Name
	Text     string      `xml:",chardata"`
	Children []plistNode `xml:",any"`
}

func darwinDevices(data []byte) ([]usbDevice, error) {
	var root plistNode
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if root.XMLName.Local != "plist" {
		return nil, fmt.Errorf("unexpected ioreg output")
	}
	var out []usbDevice
	var walk func(plistNode)
	walk = func(n plistNode) {
		if n.XMLName.Local == "dict" {
			fields := map[string]string{}
			for i := 0; i+1 < len(n.Children); i += 2 {
				if n.Children[i].XMLName.Local == "key" {
					fields[n.Children[i].Text] = n.Children[i+1].Text
				}
			}
			vendor, _ := strconv.ParseUint(strings.TrimSpace(fields["idVendor"]), 0, 64)
			pid, _ := strconv.ParseUint(strings.TrimSpace(fields["idProduct"]), 0, 64)
			if vendor == 0x05c6 && pid == 0x9008 {
				product := fields["USB Product Name"]
				if product == "" {
					product = fields["kUSBProductString"]
				}
				path := fields["locationID"]
				if path == "" {
					path = fields["IORegistryEntryID"]
				}
				out = append(out, usbDevice{Vendor: vendor, ProductID: pid, Product: product, Path: "ioreg:" + path})
			}
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(root)
	return out, nil
}
func windowsDevices(data []byte) ([]usbDevice, error) {
	var devices []struct{ Product, Path string }
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if err := json.Unmarshal(data, &devices); err != nil {
		return nil, err
	}
	out := make([]usbDevice, 0, len(devices))
	for _, d := range devices {
		out = append(out, usbDevice{Vendor: 0x05c6, ProductID: 0x9008, Product: d.Product, Path: d.Path})
	}
	return out, nil
}
