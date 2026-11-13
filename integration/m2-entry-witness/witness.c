/* Test instrumentation ONLY: post-entry observations, never launch authority. */
#if !defined(__linux__) || !defined(__x86_64__)
#error Linux x86_64 is the only reviewed witness ABI
#endif
#include <asm/unistd.h>
#include <asm-generic/fcntl.h>
#include <asm-generic/poll.h>
#include <linux/stat.h>
#include <linux/capability.h>
#include <linux/prctl.h>

#define REPORT_FD 3
#define GATE_FD 4
#define REPORT_MAX 1024
#define WAIT_MS 5000
#define AT_EMPTY_PATH_VALUE 0x1000
#define CAP_VERSION _LINUX_CAPABILITY_VERSION_3

_Static_assert(sizeof(long) == 8, "64-bit syscall ABI required");
_Static_assert(sizeof(struct statx) == 256, "statx ABI mismatch");
_Static_assert(sizeof(struct __user_cap_data_struct) == 12, "capget ABI mismatch");

static inline long call6(long number, long a, long b, long c, long d, long e, long f) {
    register long r10 __asm__("r10") = d;
    register long r8 __asm__("r8") = e;
    register long r9 __asm__("r9") = f;
    long result;
    __asm__ volatile("syscall" : "=a"(result) : "a"(number), "D"(a), "S"(b), "d"(c), "r"(r10), "r"(r8), "r"(r9) : "rcx", "r11", "memory");
    return result;
}
static __attribute__((noreturn)) void finish(long code) {
    call6(__NR_exit, code, 0, 0, 0, 0, 0);
    __asm__ volatile("ud2" ::: "memory"); /* Never fall through if exit is denied. */
    __builtin_unreachable();
}
struct stamp { long seconds; long nanoseconds; };
static long now_ms(void) {
    struct stamp value = {0, 0};
    if (call6(__NR_clock_gettime, 1, (long)&value, 0, 0, 0, 0) != 0) finish(112);
    return value.seconds * 1000 + value.nanoseconds / 1000000;
}
static int pipe_role(int fd, int access, struct statx *value) {
    long flags;
    if (call6(__NR_statx, fd, (long)"", AT_EMPTY_PATH_VALUE, STATX_TYPE | STATX_INO, (long)value, 0) != 0) return 0;
    if ((value->stx_mask & (STATX_TYPE | STATX_INO)) != (STATX_TYPE | STATX_INO) || (value->stx_mode & S_IFMT) != S_IFIFO) return 0;
    flags = call6(__NR_fcntl, fd, F_GETFL, 0, 0, 0, 0);
    if (flags < 0 || (flags & O_ACCMODE) != access) return 0;
    /* Only these caller-owned pipe endpoints' I/O flags change, not permissions. */
    return call6(__NR_fcntl, fd, F_SETFL, flags | O_NONBLOCK, 0, 0, 0) == 0;
}
static int await_io(int fd, short events, long deadline) {
    struct pollfd item = {fd, events, 0};
    while (now_ms() < deadline) {
        long result = call6(__NR_poll, (long)&item, 1, 25, 0, 0, 0);
        if (result == -4) continue; /* EINTR, same bounded observation only. */
        if (result < 0 || (item.revents & (POLLERR | POLLNVAL))) return 0;
        if (item.revents & events) return 1;
        if (item.revents & POLLHUP) return 0;
    }
    return 0;
}
struct report { char bytes[REPORT_MAX]; unsigned int used; int ok; };
static void text(struct report *report, const char *value) {
    while (*value) {
        if (report->used >= REPORT_MAX) { report->ok = 0; return; }
        report->bytes[report->used++] = *value++;
    }
}
static void decimal(struct report *report, unsigned long value) {
    char digits[21]; unsigned int length = 0;
    do { digits[length++] = (char)('0' + value % 10); value /= 10; } while (value);
    while (length) { char next[2] = {digits[--length], 0}; text(report, next); }
}
static void field(struct report *report, const char *key, unsigned long value) {
    text(report, key); text(report, "="); decimal(report, value); text(report, "\n");
}
static void capability(struct report *report, const char *key, unsigned int low, unsigned int high) {
    static const char digits[] = "0123456789abcdef";
    unsigned long value = (unsigned long)low | ((unsigned long)high << 32);
    text(report, key); text(report, "=");
    for (int shift = 60; shift >= 0; shift -= 4) { char next[2] = {digits[(value >> shift) & 15], 0}; text(report, next); }
    text(report, "\n");
}
static int emit(struct report *report, long deadline) {
    unsigned int offset = 0;
    if (!report->ok || report->used == 0 || report->used > REPORT_MAX) return 0;
    while (offset < report->used && now_ms() < deadline) {
        long result = call6(__NR_write, REPORT_FD, (long)(report->bytes + offset), report->used - offset, 0, 0, 0);
        if (result > 0) { offset += (unsigned int)result; continue; }
        if (result == -4) continue;
        if (result != -11 || !await_io(REPORT_FD, POLLOUT, deadline)) return 0;
    }
    return offset == report->used;
}
__attribute__((noreturn)) void witness_main(void) {
    struct statx output = {0}, gate = {0};
    struct __user_cap_header_struct header = {CAP_VERSION, 0};
    struct __user_cap_data_struct caps[2] = {{0,0,0},{0,0,0}};
    unsigned int ruid = 0, euid = 0, suid = 0, rgid = 0, egid = 0, sgid = 0;
    struct report report = {{0}, 0, 1};
    unsigned char byte = 0;
    long pid, groups, nnp, deadline;
    if (!pipe_role(REPORT_FD, O_WRONLY, &output) || !pipe_role(GATE_FD, O_RDONLY, &gate)) finish(111);
    if (output.stx_ino == gate.stx_ino && output.stx_dev_major == gate.stx_dev_major && output.stx_dev_minor == gate.stx_dev_minor) finish(111);
    deadline = now_ms() + WAIT_MS;
    pid = call6(__NR_getpid, 0, 0, 0, 0, 0, 0);
    groups = call6(__NR_getgroups, 0, 0, 0, 0, 0, 0);
    nnp = call6(__NR_prctl, PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0, 0);
    if (pid <= 0 || groups < 0 || (nnp != 0 && nnp != 1)) finish(112);
    if (call6(__NR_getresuid, (long)&ruid, (long)&euid, (long)&suid, 0, 0, 0) != 0 || call6(__NR_getresgid, (long)&rgid, (long)&egid, (long)&sgid, 0, 0, 0) != 0 || call6(__NR_capget, (long)&header, (long)caps, 0, 0, 0, 0) != 0) finish(112);
    text(&report, "garm-m2-post-entry-witness/v1\n");
    field(&report,"pid",(unsigned long)pid);
    field(&report,"ruid",ruid); field(&report,"euid",euid); field(&report,"suid",suid);
    field(&report,"rgid",rgid); field(&report,"egid",egid); field(&report,"sgid",sgid);
    field(&report,"groups_count",(unsigned long)groups); field(&report,"no_new_privs",(unsigned long)nnp);
    capability(&report,"cap_inheritable",caps[0].inheritable,caps[1].inheritable);
    capability(&report,"cap_permitted",caps[0].permitted,caps[1].permitted);
    capability(&report,"cap_effective",caps[0].effective,caps[1].effective);
    text(&report,"birth_qualified=false\nexecution_authority=none\nend\n");
    if (!emit(&report,deadline)) finish(113);
    while (now_ms() < deadline) {
        long result = call6(__NR_read,GATE_FD,(long)&byte,1,0,0,0);
        if (result == 1) finish(byte == 0xa5 ? 0 : 115);
        if (result == -4) continue;
        if (result != -11 || !await_io(GATE_FD,POLLIN,deadline)) finish(114);
    }
    finish(114);
}
