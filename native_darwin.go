//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>

typedef CFTypeRef (*copy_exclusions_fn)(void);
typedef int (*set_exclusion_fn)(CFStringRef, unsigned int);
static void *metadata_handle;
static copy_exclusions_fn copy_exclusions;
static set_exclusion_fn set_exclusion;
static int load_metadata(void) {
 // Initialization is locked by nativeMu; successful symbols stay immutable
 // because bridge calls read them after releasing that initialization lock.
 if (copy_exclusions && set_exclusion) return 1;
 if (!metadata_handle) metadata_handle=dlopen("/System/Library/Frameworks/CoreServices.framework/Frameworks/Metadata.framework/Metadata",RTLD_LAZY|RTLD_LOCAL);
 if (!metadata_handle) return 0;
 copy_exclusions=(copy_exclusions_fn)dlsym(metadata_handle,"_MDCopyExclusionList");
 set_exclusion=(set_exclusion_fn)dlsym(metadata_handle,"_MDSetExclusion");
 return copy_exclusions && set_exclusion;
}
static uintptr_t cp_copy(void){return (uintptr_t)copy_exclusions();}
static uintptr_t cp_data(uintptr_t p){return (uintptr_t)CFPropertyListCreateData(NULL,(CFPropertyListRef)p,kCFPropertyListXMLFormat_v1_0,0,NULL);}
static long cp_length(uintptr_t p){return CFDataGetLength((CFDataRef)p);}
static const void *cp_bytes(uintptr_t p){return CFDataGetBytePtr((CFDataRef)p);}
static void cp_release(uintptr_t p){CFRelease((CFTypeRef)p);}
static uintptr_t cp_string(char *p){return (uintptr_t)CFStringCreateWithCString(NULL,p,kCFStringEncodingUTF8);}
static int cp_set(uintptr_t p,int excluded){return set_exclusion((CFStringRef)p,excluded);}
static int cp_backup(char *path,int is_dir,int excluded,char **message){
 // Foundation registers the filesystem resource-property implementation.
 static void *foundation;
 if(!foundation)foundation=dlopen("/System/Library/Frameworks/Foundation.framework/Foundation",RTLD_LAZY|RTLD_LOCAL);
 if(!foundation){*message=strdup("The macOS backup resource API is unavailable");return 0;}
 CFURLRef url=CFURLCreateFromFileSystemRepresentation(NULL,(const UInt8*)path,strlen(path),is_dir);
 if(!url){*message=strdup("Could not create a file URL");return 0;}
 CFErrorRef error=NULL;
 Boolean ok=CFURLSetResourcePropertyForKey(url,kCFURLIsExcludedFromBackupKey,excluded?kCFBooleanTrue:kCFBooleanFalse,&error);
 if(!ok){
  CFStringRef description=error?CFErrorCopyDescription(error):NULL;
  if(description){
   CFIndex size=CFStringGetMaximumSizeForEncoding(CFStringGetLength(description),kCFStringEncodingUTF8)+1;
   if(size>0 && size<=1024*1024){char *buffer=malloc(size);if(buffer){if(CFStringGetCString(description,buffer,size,kCFStringEncodingUTF8))*message=buffer;else free(buffer);}}
   CFRelease(description);
  }
  if(!*message)*message=strdup("no error description provided");
 }
 if(error)CFRelease(error);
 CFRelease(url);
 return ok;
}
*/
import "C"
import (
	"errors"
	"fmt"
	"os"
	"sync"
	"unsafe"
)

var nativeMu sync.Mutex

func loadSpotlightBridge() (spotlightBridge, error) {
	nativeMu.Lock()
	ok := C.load_metadata()
	nativeMu.Unlock()
	if ok == 0 {
		return spotlightBridge{}, errors.New("The macOS Search Privacy API is unavailable")
	}
	return spotlightBridge{
		copyList:   func() uintptr { return uintptr(C.cp_copy()) },
		createData: func(p uintptr) uintptr { return uintptr(C.cp_data(C.uintptr_t(p))) },
		dataBytes: func(p uintptr) ([]byte, error) {
			length := C.cp_length(C.uintptr_t(p))
			address := C.cp_bytes(C.uintptr_t(p))
			if length < 0 || length > 16*1024*1024 || address == nil {
				return nil, errors.New("Search Privacy returned invalid list data")
			}
			return C.GoBytes(address, C.int(length)), nil
		},
		release: func(p uintptr) { C.cp_release(C.uintptr_t(p)) },
		createString: func(s string) uintptr {
			raw := C.CString(s)
			defer C.free(unsafe.Pointer(raw))
			return uintptr(C.cp_string(raw))
		},
		set: func(p uintptr, excluded bool) int {
			v := C.int(0)
			if excluded {
				v = 1
			}
			return int(C.cp_set(C.uintptr_t(p), v))
		},
	}, nil
}
func nativeBackupWrite(path string, excluded bool) error {
	raw := C.CString(path)
	defer C.free(unsafe.Pointer(raw))
	flag, isDir := C.int(0), C.int(0)
	if excluded {
		flag = 1
	}
	if info, e := os.Stat(path); e == nil && info.IsDir() {
		isDir = 1
	}
	var message *C.char
	nativeMu.Lock()
	ok := C.cp_backup(raw, isDir, flag, &message)
	nativeMu.Unlock()
	if message != nil {
		defer C.free(unsafe.Pointer(message))
	}
	if ok == 0 {
		detail := "no error details provided"
		if message != nil {
			detail = C.GoString(message)
		}
		return fmt.Errorf("Could not set backup exclusion for %q: %s", path, detail)
	}
	return nil
}
