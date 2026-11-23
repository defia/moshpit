//go:build darwin && cgo

// Copyright 2025.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package keychain

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <stdlib.h>

static CFMutableDictionaryRef password_query(CFStringRef account) {
	CFMutableDictionaryRef query = CFDictionaryCreateMutable(NULL, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(query, kSecAttrService, CFSTR("com.github.defia.moshpit.ssh-password"));
	CFDictionarySetValue(query, kSecAttrAccount, account);
	return query;
}

static OSStatus read_password_item(CFStringRef account, Boolean with_password, CFTypeRef *result) {
	CFMutableDictionaryRef query = password_query(account);
	CFDictionarySetValue(query, kSecReturnAttributes, kCFBooleanTrue);
	if (with_password)
		CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
	OSStatus status = SecItemCopyMatching(query, result);
	CFRelease(query);
	return status;
}

static OSStatus save_password_item(CFStringRef account, CFStringRef binding, CFDataRef password) {
	CFMutableDictionaryRef query = password_query(account);
	CFMutableDictionaryRef item = CFDictionaryCreateMutableCopy(NULL, 0, query);
	CFDictionarySetValue(item, kSecAttrComment, binding);
	CFDictionarySetValue(item, kSecValueData, password);
	OSStatus status = SecItemAdd(item, NULL);
	if (status == errSecDuplicateItem) {
		CFDictionaryRemoveValue(item, kSecClass);
		CFDictionaryRemoveValue(item, kSecAttrService);
		CFDictionaryRemoveValue(item, kSecAttrAccount);
		status = SecItemUpdate(query, item);
	}
	CFRelease(item);
	CFRelease(query);
	return status;
}

static OSStatus delete_password_item(CFStringRef account) {
	CFMutableDictionaryRef query = password_query(account);
	OSStatus status = SecItemDelete(query);
	CFRelease(query);
	return status;
}

static Boolean binding_matches(CFDictionaryRef item, CFStringRef binding) {
	CFTypeRef stored = CFDictionaryGetValue(item, kSecAttrComment);
	return stored != NULL && CFEqual(stored, binding);
}

static CFDataRef item_password(CFDictionaryRef item) {
	return (CFDataRef)CFDictionaryGetValue(item, kSecValueData);
}
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/taylorbanks/moshpit/internal/core/ports"
)

type store struct{}

func New() ports.PasswordStore { return store{} }

func cfString(value string) C.CFStringRef {
	text := C.CString(value)
	defer C.free(unsafe.Pointer(text))
	return C.CFStringCreateWithCString(0, text, C.kCFStringEncodingUTF8)
}

func (store) Has(alias, binding string) (bool, error) {
	item, err := readItem(alias, false)
	if err != nil || item == 0 {
		return false, err
	}
	defer C.CFRelease(C.CFTypeRef(item))
	if binding == "" {
		return true, nil
	}
	value := cfString(binding)
	defer C.CFRelease(C.CFTypeRef(value))
	return C.binding_matches(item, value) != 0, nil
}

func (store) Read(alias, binding string) ([]byte, error) {
	item, err := readItem(alias, true)
	if err != nil || item == 0 {
		return nil, err
	}
	defer C.CFRelease(C.CFTypeRef(item))
	value := cfString(binding)
	defer C.CFRelease(C.CFTypeRef(value))
	if C.binding_matches(item, value) == 0 {
		return nil, nil
	}
	password := C.item_password(item)
	if password == 0 {
		return nil, nil
	}
	return C.GoBytes(unsafe.Pointer(C.CFDataGetBytePtr(password)), C.int(C.CFDataGetLength(password))), nil
}

func readItem(alias string, withPassword bool) (C.CFDictionaryRef, error) {
	account := cfString(alias)
	defer C.CFRelease(C.CFTypeRef(account))
	var result C.CFTypeRef
	var readData C.Boolean
	if withPassword {
		readData = 1
	}
	status := C.read_password_item(account, readData, &result)
	if status == C.errSecItemNotFound {
		return 0, nil
	}
	return C.CFDictionaryRef(result), statusError(status)
}

func (store) Save(alias, binding string, password []byte) error {
	account, value := cfString(alias), cfString(binding)
	defer C.CFRelease(C.CFTypeRef(account))
	defer C.CFRelease(C.CFTypeRef(value))
	data := C.CFDataCreate(0, (*C.UInt8)(unsafe.Pointer(&password[0])), C.CFIndex(len(password)))
	defer C.CFRelease(C.CFTypeRef(data))
	return statusError(C.save_password_item(account, value, data))
}

func (store) Delete(alias string) error {
	account := cfString(alias)
	defer C.CFRelease(C.CFTypeRef(account))
	status := C.delete_password_item(account)
	if status == C.errSecItemNotFound {
		return nil
	}
	return statusError(status)
}

func statusError(status C.OSStatus) error {
	if status == C.errSecSuccess {
		return nil
	}
	return fmt.Errorf("macOS Keychain error (%d)", int(status))
}
