package server

/*
#include <libproc.h>
#include <string.h>

static int rex_process_cwd(int pid, char *out, int size) {
    struct proc_vnodepathinfo info;
    if (proc_pidinfo(pid, PROC_PIDVNODEPATHINFO, 0, &info, sizeof(info)) != sizeof(info)) return -1;
    return strlcpy(out, info.pvi_cdir.vip_path, size) < size ? 0 : -1;
}

static int rex_process_name(int pid, char *out, int size) {
    return proc_name(pid, out, size);
}

static int rex_process_details(int pid, int *uid, int *parent, char *path, int size) {
    struct proc_bsdinfo info;
    if (proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info)) != sizeof(info)) return -1;
    *uid = info.pbi_uid;
    *parent = info.pbi_ppid;
    if (proc_pidpath(pid, path, size) <= 0) path[0] = 0;
    return 0;
}
*/
import "C"

import "fmt"

func processCwd(pid int) (string, error) {
	var path [4096]C.char
	if C.rex_process_cwd(C.int(pid), &path[0], C.int(len(path))) != 0 {
		return "", fmt.Errorf("working directory unavailable for pid %d", pid)
	}
	return C.GoString(&path[0]), nil
}

func processName(pid int) string {
	var name [4096]C.char
	if C.rex_process_name(C.int(pid), &name[0], C.int(len(name))) <= 0 {
		return ""
	}
	return C.GoString(&name[0])
}

func processDetails(pid int) (object, int, error) {
	var uid, parent C.int
	var path [4096]C.char
	if C.rex_process_details(C.int(pid), &uid, &parent, &path[0], C.int(len(path))) != 0 {
		return nil, 0, fmt.Errorf("process unavailable for pid %d", pid)
	}
	result := object{"pid": pid, "user_id": int(uid), "name": processName(pid)}
	if value := C.GoString(&path[0]); value != "" {
		result["path"] = value
	}
	if cwd, err := processCwd(pid); err == nil {
		result["cwd"] = cwd
	}
	return result, int(parent), nil
}
