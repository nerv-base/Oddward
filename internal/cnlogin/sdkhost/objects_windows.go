// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package sdkhost

import (
	"encoding/binary"
	"golang.org/x/sys/windows"
	"runtime"
	"unicode/utf16"
	"unsafe"
)

const mappingSize = 1 << 20

func privateSecurity() (*windows.SecurityAttributes, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, nil
}

func (h *Host) createObjects(readyName, instanceName string) error {
	sa, err := privateSecurity()
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(sa)
	name, _ := windows.UTF16PtrFromString(instanceName)
	h.instance, err = windows.CreateMutex(sa, false, name)
	if err != nil {
		return objectError("singleton mutex", err)
	}
	name, _ = windows.UTF16PtrFromString(h.env.MapName)
	h.mapping, err = windows.CreateFileMapping(windows.InvalidHandle, sa, windows.PAGE_READWRITE, 0, mappingSize, name)
	if err != nil {
		return objectError("mapping", err)
	}
	h.view, err = windows.MapViewOfFile(h.mapping, windows.FILE_MAP_WRITE, 0, 0, mappingSize)
	if err != nil {
		return err
	}
	name, _ = windows.UTF16PtrFromString(h.env.MutexName)
	h.mapMutex, err = windows.CreateMutex(sa, false, name)
	if err != nil {
		return objectError("mapping mutex", err)
	}
	name, _ = windows.UTF16PtrFromString(readyName)
	h.ready, err = windows.CreateEvent(sa, 1, 1, name)
	if err != nil {
		return objectError("readiness event", err)
	}
	name, _ = windows.UTF16PtrFromString(h.contextName)
	h.contextEvent, err = windows.CreateEvent(sa, 1, 0, name)
	if err != nil {
		return objectError("context event", err)
	}
	h.stopEvent, err = windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	name, _ = windows.UTF16PtrFromString(`\\.\pipe\` + h.pipeName)
	h.pipe, err = windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE, windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 65536, 65536, 0, sa)
	if err != nil {
		return objectError("private pipe", err)
	}
	data := mappingBytes(h.view)
	binary.LittleEndian.PutUint32(data, 1)
	binary.LittleEndian.PutUint32(data[8:], 1289)
	binary.LittleEndian.PutUint16(data[12:], uint16(h.env.HostPID))
	writeWide(data[14:14+520], h.contextName)
	writeWide(data[0x316:0x716], "1289")
	binary.LittleEndian.PutUint32(data[0x716:], 1)
	copy(data[0x922:], h.contextName)
	return nil
}

func mappingBytes(view uintptr) []byte {
	return unsafe.Slice((*byte)(unsafe.Add(nil, view)), mappingSize)
}
func writeWide(dst []byte, s string) {
	for i, u := range utf16.Encode([]rune(s)) {
		binary.LittleEndian.PutUint16(dst[2*i:], u)
	}
}
