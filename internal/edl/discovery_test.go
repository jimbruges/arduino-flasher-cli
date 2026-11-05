// This file is part of arduino-flasher-cli.
//
// SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
// SPDX-License-Identifier: GPL-3.0-or-later

package edl

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAddressable(t *testing.T) {
	a := usbDevice{Vendor: 0x05c6, ProductID: 0x9008, Product: "QUSB_BULK_CID:0420_SN:8ED963AB", Path: "port1"}
	b := usbDevice{Vendor: 0x05c6, ProductID: 0x9008, Product: "QUSB_BULK_CID:0420_SN:11111111", Path: "port2"}
	normal := usbDevice{Vendor: 0x2341, ProductID: 0x78, Product: "Uno Q _ uno_q", Path: "port3"}
	crash := a
	crash.ProductID = 0x900e
	crash.Path = "port4"
	result, err := addressable([]usbDevice{a, b, a, normal, crash})
	if err != nil || len(result) != 2 || result[0].Serial != "0x11111111" || result[1].Serial != "0x8ED963AB" {
		t.Fatalf("%+v %v", result, err)
	}
	for _, product := range []string{"QUSB_BULK", "QUSB_BULK_SN:8ED963AB!", "QUSB_BULK_SN:8ed963ab", "QUSB_BULK_SN:1111", "QUSB_BULK_SN:FFFFFFFFFFFFFFFF"} {
		d := a
		d.Product = product
		if _, err := addressable([]usbDevice{d}); err == nil {
			t.Fatalf("accepted %q", product)
		}
	}
	duplicate := a
	duplicate.Path = "other-port"
	if _, err := addressable([]usbDevice{a, duplicate}); err == nil {
		t.Fatal("accepted ambiguous serial")
	}
}

func TestDarwinEmptyInventory(t *testing.T) {
	for _, data := range [][]byte{nil, {}, []byte(" \n")} {
		candidates, err := darwinDevices(data)
		if err != nil {
			t.Fatal(err)
		}
		devices, err := addressable(candidates)
		if err != nil || len(devices) != 0 {
			t.Fatalf("%v %v", devices, err)
		}
	}
}

func TestDarwinInventory(t *testing.T) {
	// Mirrors ioreg's plist shape, including nested children and irrelevant binary data.
	data := []byte(`<?xml version="1.0"?><plist version="1.0"><array><dict>
 <key>idVendor</key><integer>1478</integer><key>idProduct</key><integer>36872</integer>
 <key>USB Product Name</key><string>QUSB_BULK_CID:0420_SN:11111111</string>
 <key>locationID</key><integer>35651584</integer><key>Blob</key><data>AA==</data>
 <key>IORegistryEntryChildren</key><array><dict><key>idVendor</key><integer>9025</integer><key>idProduct</key><integer>120</integer></dict></array>
 </dict></array></plist>`)
	devices, err := darwinDevices(data)
	if err != nil {
		t.Fatal(err)
	}
	result, err := addressable(devices)
	if err != nil || len(result) != 1 || result[0].Serial != "0x11111111" || result[0].Path != "ioreg:35651584" {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := darwinDevices([]byte("invalid")); err == nil {
		t.Fatal("accepted invalid plist")
	}
}
func TestLinuxInventory(t *testing.T) {
	root := t.TempDir()
	for name, attrs := range map[string]map[string]string{
		"1-1":     {"idVendor": "05c6\n", "idProduct": "9008\n", "product": "QUSB_BULK_CID:0420_SN:11111111\n"},
		"1-2":     {"idVendor": "2341\n", "idProduct": "0078\n", "product": "Uno Q _ uno_q\n"},
		"1-1:1.0": {},
	} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for key, value := range attrs {
			if err := os.WriteFile(filepath.Join(dir, key), []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	devices, err := linuxDevices(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := addressable(devices)
	if err != nil || len(result) != 1 || result[0].Serial != "0x11111111" {
		t.Fatalf("%+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := linuxDevices(ctx, root); err == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestWindowsInventory(t *testing.T) {
	data := []byte(`[{"Product":"QUSB_BULK_CID:0420_SN:8ED963AB","Path":"USB\\VID_05C6&PID_9008\\1"}]`)
	devices, err := windowsDevices(data)
	if err != nil {
		t.Fatal(err)
	}
	result, err := addressable(devices)
	if err != nil || len(result) != 1 || result[0].Serial != "0x8ED963AB" {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := windowsDevices([]byte("powershell failed")); err == nil {
		t.Fatal("accepted invalid discovery output")
	}
	devices, err = windowsDevices([]byte("[]"))
	if err != nil || len(devices) != 0 {
		t.Fatal("empty inventory failed")
	}
}
