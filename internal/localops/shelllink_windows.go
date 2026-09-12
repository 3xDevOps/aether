//go:build windows

package localops

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shellLinkOLE32            = windows.NewLazySystemDLL("ole32.dll")
	shellLinkCoInitializeEx   = shellLinkOLE32.NewProc("CoInitializeEx")
	shellLinkCoUninitialize   = shellLinkOLE32.NewProc("CoUninitialize")
	shellLinkCoCreateInstance = shellLinkOLE32.NewProc("CoCreateInstance")

	shellLinkCLSID = windows.GUID{
		Data1: 0x00021401,
		Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46},
	}
	shellLinkIID = windows.GUID{
		Data1: 0x000214f9,
		Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46},
	}
	shellLinkPersistFileIID = windows.GUID{
		Data1: 0x0000010b,
		Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46},
	}
)

const shellLinkInprocServer = 1

// COM interfaces share IUnknown slots 0-2. The remaining offsets are from
// IShellLinkW and IPersistFile in the Windows SDK.
const (
	shellLinkQueryInterfaceSlot      = 0
	shellLinkReleaseSlot             = 2
	shellLinkSetWorkingDirectorySlot = 9
	shellLinkSetPathSlot             = 20
	shellLinkPersistFileSaveSlot     = 6
)

type shellLinkCOM struct {
	vtable *[21]uintptr
}

func (object *shellLinkCOM) release() {
	syscall.SyscallN(object.vtable[shellLinkReleaseSlot], uintptr(unsafe.Pointer(object)))
}

func shellLinkHRESULT(operation string, result uintptr) error {
	// COM returns HRESULT, not GetLastError. Every nonnegative HRESULT succeeds.
	if code := uint32(result); int32(code) < 0 {
		return fmt.Errorf("%s failed with HRESULT 0x%08x", operation, code)
	}
	return nil
}

func writeShellLink(path, target, workingDir string) error {
	linkPath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode shortcut path: %w", err)
	}
	targetPath, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return fmt.Errorf("encode shortcut target: %w", err)
	}
	workingDirectory, err := windows.UTF16PtrFromString(workingDir)
	if err != nil {
		return fmt.Errorf("encode shortcut working directory: %w", err)
	}
	for _, proc := range []*windows.LazyProc{shellLinkCoInitializeEx, shellLinkCoUninitialize, shellLinkCoCreateInstance} {
		if err := proc.Find(); err != nil {
			return fmt.Errorf("resolve ole32.dll!%s: %w", proc.Name, err)
		}
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	result, _, _ := shellLinkCoInitializeEx.Call(0, windows.COINIT_APARTMENTTHREADED)
	if err := shellLinkHRESULT("CoInitializeEx", result); err != nil {
		return err
	}
	// S_OK and S_FALSE both require CoUninitialize on this same OS thread.
	defer shellLinkCoUninitialize.Call()

	var link *shellLinkCOM
	result, _, _ = shellLinkCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&shellLinkCLSID)), 0, shellLinkInprocServer,
		uintptr(unsafe.Pointer(&shellLinkIID)), uintptr(unsafe.Pointer(&link)),
	)
	if err := shellLinkHRESULT("CoCreateInstance(CLSID_ShellLink)", result); err != nil {
		return err
	}
	defer link.release()

	result, _, _ = syscall.SyscallN(link.vtable[shellLinkSetPathSlot], uintptr(unsafe.Pointer(link)), uintptr(unsafe.Pointer(targetPath)))
	if err := shellLinkHRESULT("IShellLinkW.SetPath", result); err != nil {
		return err
	}
	result, _, _ = syscall.SyscallN(link.vtable[shellLinkSetWorkingDirectorySlot], uintptr(unsafe.Pointer(link)), uintptr(unsafe.Pointer(workingDirectory)))
	if err := shellLinkHRESULT("IShellLinkW.SetWorkingDirectory", result); err != nil {
		return err
	}

	var persist *shellLinkCOM
	result, _, _ = syscall.SyscallN(
		link.vtable[shellLinkQueryInterfaceSlot], uintptr(unsafe.Pointer(link)),
		uintptr(unsafe.Pointer(&shellLinkPersistFileIID)), uintptr(unsafe.Pointer(&persist)),
	)
	if err := shellLinkHRESULT("IShellLinkW.QueryInterface(IPersistFile)", result); err != nil {
		return err
	}
	defer persist.release()
	result, _, _ = syscall.SyscallN(persist.vtable[shellLinkPersistFileSaveSlot], uintptr(unsafe.Pointer(persist)), uintptr(unsafe.Pointer(linkPath)), 1)
	return shellLinkHRESULT("IPersistFile.Save", result)
}
