/* VM-ONLY TEST SOURCE. Default build emits a NON-EXECUTABLE ET_REL object.
 * No bootstrap/actor/API/ledger. NEVER run on a live or unapproved host.
 * A passing fixed-witness experiment is NOT sandbox/job/release authority. */
#if !defined(__linux__) || !defined(__x86_64__)
#error Only Linux x86_64 native test ABI is reviewed
#endif
#include <asm/unistd.h>
#include <asm/statfs.h>
#include <asm/siginfo.h>
#include <asm-generic/poll.h>
#include <linux/stat.h>
#include <linux/fcntl.h>
#include <linux/memfd.h>
#include <linux/magic.h>
#include <linux/sched.h>
#include <linux/wait.h>
#if !defined(MFD_EXEC) || !defined(F_SEAL_EXEC)
#error Reviewed 6.3+ memfd execution/seal UAPI headers are required; no fallback
#endif
#define OUTER_REPORT 3
#define OUTER_GATE 4
#define TARGET_CGROUP 5
#define EMPTY_PATH 0x1000
#define CLOEXEC_RANGE 4U
#define MAX_IMAGE 65536UL
#define MAX_REPORT 1024UL
#define CHILD_MS 5000L
#define TOTAL_MS 15000L
#define REQUIRED_SEALS (F_SEAL_WRITE | F_SEAL_GROW | F_SEAL_SHRINK | F_SEAL_EXEC | F_SEAL_SEAL)

_Static_assert(sizeof(long) == 8, "64-bit ABI");
_Static_assert(sizeof(struct clone_args) == 88, "review changed clone3 ABI");
_Static_assert(sizeof(siginfo_t) == 128, "review changed waitid ABI");
_Static_assert(__builtin_offsetof(struct clone_args, cgroup) == 80, "cgroup offset");
extern const unsigned char _binary_witness_entry_witness_start[];
extern const unsigned char _binary_witness_entry_witness_end[];

static inline long sys6(long nr, long a, long b, long c, long d, long e, long f) {
    register long r10 __asm__("r10") = d;
    register long r8 __asm__("r8") = e;
    register long r9 __asm__("r9") = f;
    long result;
    __asm__ volatile("syscall" : "=a"(result) : "a"(nr), "D"(a), "S"(b), "d"(c), "r"(r10), "r"(r8), "r"(r9) : "rcx", "r11", "memory");
    return result;
}
static __attribute__((noreturn)) void child_exit(long code) {
    sys6(__NR_exit,code,0,0,0,0,0);
    __asm__ volatile("ud2" ::: "memory");
    __builtin_unreachable();
}
struct stamp { long seconds, nanoseconds; };
static long now_ms(void) {
    struct stamp t = {0,0};
    if (sys6(__NR_clock_gettime,1,(long)&t,0,0,0,0) != 0) return -1;
    return t.seconds * 1000 + t.nanoseconds / 1000000;
}
static int before(long deadline) { long n = now_ms(); return n >= 0 && n < deadline; }
static void close_fd(int fd) { if (fd >= 0) sys6(__NR_close,fd,0,0,0,0,0); }
static int pipe_role(int fd, int mode, struct statx *s) {
    long flags;
    if (sys6(__NR_statx,fd,(long)"",EMPTY_PATH,STATX_TYPE | STATX_INO,(long)s,0) != 0 || (s->stx_mask & (STATX_TYPE | STATX_INO)) != (STATX_TYPE | STATX_INO) || (s->stx_mode & S_IFMT) != S_IFIFO) return 0;
    flags = sys6(__NR_fcntl,fd,F_GETFL,0,0,0,0);
    return flags >= 0 && (flags & O_ACCMODE) == mode && sys6(__NR_fcntl,fd,F_SETFL,flags | O_NONBLOCK,0,0,0) == 0;
}
static int poll_for(int fd, short events, long deadline) {
    struct pollfd p = {fd,events,0};
    while (before(deadline)) {
        long n = sys6(__NR_poll,(long)&p,1,25,0,0,0);
        if (n == -4) continue;
        if (n < 0 || (p.revents & (POLLERR | POLLNVAL))) return 0;
        if (p.revents & events) return 1;
        if (p.revents & POLLHUP) return 0;
    }
    return 0;
}
static int write_bytes(int fd, const unsigned char *bytes, unsigned long length, long deadline) {
    unsigned long offset = 0;
    while (offset < length && before(deadline)) {
        long n = sys6(__NR_write,fd,(long)(bytes + offset),length - offset,0,0,0);
        if (n > 0) { offset += (unsigned long)n; continue; }
        if (n == -4) continue;
        if (n != -11 || !poll_for(fd,POLLOUT,deadline)) return 0;
    }
    return offset == length;
}
static int read_byte(int fd, unsigned char *byte, long deadline) {
    while (before(deadline)) {
        long n = sys6(__NR_read,fd,(long)byte,1,0,0,0);
        if (n == 1) return 1;
        if (n == -4) continue;
        if (n != -11 || !poll_for(fd,POLLIN,deadline)) return 0;
    }
    return 0;
}
static unsigned long text_size(const char *s) { unsigned long n = 0; while (s[n]) ++n; return n; }
static int same(const unsigned char *a, const unsigned char *b, unsigned long n) { for (unsigned long i=0;i<n;++i) if (a[i]!=b[i]) return 0; return 1; }
static unsigned long pid_line(unsigned char *out, unsigned long pid) {
    unsigned char digits[21]; unsigned long n=0,length=0;
    do { digits[n++]=(unsigned char)('0'+pid%10); pid/=10; } while (pid);
    while (n) out[length++]=digits[--n];
    out[length++]='\n'; return length;
}
static int phase(const char *name, unsigned long pid, long deadline) {
    static const char prefix[]="garm-m2-native-parent-test/v1\n";
    static const char boundary[]="birth_qualified=false\nexecution_authority=none\nend\n";
    unsigned char line[22]; unsigned long length=pid_line(line,pid);
    return write_bytes(OUTER_REPORT,(const unsigned char *)prefix,sizeof(prefix)-1,deadline) && write_bytes(OUTER_REPORT,(const unsigned char *)name,text_size(name),deadline) && write_bytes(OUTER_REPORT,(const unsigned char *)"\npid=",5,deadline) && write_bytes(OUTER_REPORT,line,length,deadline) && write_bytes(OUTER_REPORT,(const unsigned char *)boundary,sizeof(boundary)-1,deadline);
}
/* Names below are private literals in trusted cgroup2, not caller paths. */
static long read_control(const char *name, unsigned char *out, unsigned long maximum, long deadline) {
    struct statx s = {0}; unsigned long used=0;
    int fd=(int)sys6(__NR_openat,TARGET_CGROUP,(long)name,O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC,0,0,0);
    if (fd < 0) return -1;
    if (sys6(__NR_statx,fd,(long)"",EMPTY_PATH,STATX_TYPE,(long)&s,0) != 0 || !(s.stx_mask & STATX_TYPE) || (s.stx_mode & S_IFMT) != S_IFREG) { close_fd(fd); return -1; }
    while (before(deadline)) {
        unsigned char extra; long n;
        if (used == maximum) { n=sys6(__NR_read,fd,(long)&extra,1,0,0,0); close_fd(fd); return n==0 ? (long)used : -1; }
        n=sys6(__NR_read,fd,(long)(out+used),maximum-used,0,0,0);
        if (n == 0) { close_fd(fd); return (long)used; }
        if (n > 0) { used+=(unsigned long)n; continue; }
        if (n == -4) continue;
        if (n != -11 || !poll_for(fd,POLLIN,deadline)) break;
    }
    close_fd(fd); return -1;
}
static int target_is_empty_domain(long deadline) {
    struct statx s={0}; struct statfs fs={0}; unsigned char bytes[64]; long n;
    if (sys6(__NR_statx,TARGET_CGROUP,(long)"",EMPTY_PATH,STATX_TYPE,(long)&s,0) != 0 || !(s.stx_mask & STATX_TYPE) || (s.stx_mode & S_IFMT) != S_IFDIR || sys6(__NR_fstatfs,TARGET_CGROUP,(long)&fs,0,0,0,0) != 0 || fs.f_type != CGROUP2_SUPER_MAGIC) return 0;
    n=read_control("cgroup.type",bytes,sizeof(bytes),deadline);
    if (n!=7 || !same(bytes,(const unsigned char *)"domain\n",7)) return 0;
    /* Empty direct membership is an observation, NOT authorization/whole-tree or writer fencing. A fresh sole-authority LEAF is an external VM prerequisite. */
    return read_control("cgroup.procs",bytes,sizeof(bytes),deadline)==0;
}
static int target_has_only_child(unsigned long pid, long deadline) {
    unsigned char expected[22],bytes[64]; unsigned long length=pid_line(expected,pid);
    long n=read_control("cgroup.procs",bytes,sizeof(bytes),deadline);
    return n==(long)length && same(bytes,expected,length);
}
/* Copy ONLY the fixed compiled-in witness. No caller executable/path/argv/env. */
static int sealed_image(long deadline) {
    struct statx s={0};
    unsigned long start=(unsigned long)_binary_witness_entry_witness_start;
    unsigned long end=(unsigned long)_binary_witness_entry_witness_end;
    unsigned long size,offset=0; long seals; int fd;
    if (end <= start || (size=end-start)>MAX_IMAGE) return -1;
    fd=(int)sys6(__NR_memfd_create,(long)"garm-m2-fixed-witness",MFD_CLOEXEC | MFD_ALLOW_SEALING | MFD_EXEC,0,0,0,0);
    if (fd <= TARGET_CGROUP) { close_fd(fd); return -1; }
    while (offset<size && before(deadline)) {
        long n=sys6(__NR_pwrite64,fd,(long)(start+offset),size-offset,offset,0,0);
        if (n>0) { offset+=(unsigned long)n; continue; }
        if (n == -4) continue;
        close_fd(fd); return -1;
    }
    if (offset!=size) { close_fd(fd); return -1; }
    if (sys6(__NR_fchmod,fd,0500,0,0,0,0) != 0 || sys6(__NR_fcntl,fd,F_ADD_SEALS,REQUIRED_SEALS,0,0,0) != 0) { close_fd(fd); return -1; }
    seals=sys6(__NR_fcntl,fd,F_GET_SEALS,0,0,0,0);
    if (seals<0 || (seals & REQUIRED_SEALS)!=REQUIRED_SEALS || sys6(__NR_statx,fd,(long)"",EMPTY_PATH,STATX_TYPE | STATX_MODE | STATX_SIZE,(long)&s,0) != 0 || (s.stx_mask & (STATX_TYPE | STATX_MODE | STATX_SIZE))!=(STATX_TYPE | STATX_MODE | STATX_SIZE) || s.stx_size!=size || (s.stx_mode & S_IFMT)!=S_IFREG || (s.stx_mode & 07777)!=0500) { close_fd(fd); return -1; }
    return fd;
}
static int new_pipe(int pair[2]) {
    if (sys6(__NR_pipe2,(long)pair,O_CLOEXEC | O_NONBLOCK,0,0,0,0)!=0) return 0;
    if (pair[0]<=TARGET_CGROUP || pair[1]<=TARGET_CGROUP) { close_fd(pair[0]);close_fd(pair[1]);pair[0]=pair[1]=-1;return 0; }
    return 1;
}
static int child_still_waiting(int pidfd) {
    struct pollfd p={pidfd,POLLIN,0};
    return sys6(__NR_poll,(long)&p,1,0,0,0,0)==0 && p.revents==0;
}
static int wait_child(int pidfd, unsigned long pid, long deadline) {
    while (before(deadline)) {
        siginfo_t information={0};
        long n=sys6(__NR_waitid,P_PIDFD,pidfd,(long)&information,WEXITED | WNOHANG,0,0);
        if (n == -4) continue;
        if (n != 0) return 0;
        if (information.si_pid != 0) return (unsigned long)information.si_pid==pid && information.si_code==1 && information.si_status==0;
        if (!poll_for(pidfd,POLLIN,deadline)) return 0;
    }
    return 0;
}
static int witness_report(int fd, unsigned long pid, unsigned char *bytes, unsigned long *length, long deadline) {
    unsigned long used=0;
    while (used<MAX_REPORT && before(deadline)) {
        if (!read_byte(fd,bytes+used,deadline)) return 0;
        ++used;
        if (used>=5 && same(bytes+used-5,(const unsigned char *)"\nend\n",5)) {
            static const char header[]="garm-m2-post-entry-witness/v1\npid=";
            static const char boundary[]="\nbirth_qualified=false\nexecution_authority=none\nend\n";
            unsigned char line[22]; unsigned long line_size=pid_line(line,pid);
            unsigned long prefix=sizeof(header)-1, suffix=sizeof(boundary)-1;
            if (used<prefix+line_size+suffix || !same(bytes,(const unsigned char *)header,prefix) || !same(bytes+prefix,line,line_size) || !same(bytes+used-suffix,(const unsigned char *)boundary,suffix)) return 0;
            *length=used;return 1;
        }
    }
    return 0;
}
static __attribute__((noreturn)) void child_path(int image,int report[2],int birth[2],int gate[2]) {
    unsigned char byte=0; long deadline=now_ms();
    static const char *const argv[]={"fixed-m2-post-entry-witness",0};
    static const char *const env[]={"PATH=/no-tools","HOME=/no-home",0};
    if (deadline<0) child_exit(121);
    deadline+=CHILD_MS;
    close_fd(report[0]);close_fd(birth[1]);close_fd(gate[1]);close_fd(TARGET_CGROUP);
    if (sys6(__NR_dup3,report[1],3,0,0,0,0)<0 || sys6(__NR_dup3,gate[0],4,0,0,0,0)<0) child_exit(122);
    if (!write_bytes(3,(const unsigned char *)"B",1,deadline) || !read_byte(birth[0],&byte,deadline) || byte!=0xa5) child_exit(123);
    if (sys6(__NR_close_range,5,0xffffffffU,CLOEXEC_RANGE,0,0,0)!=0) child_exit(124);
    /* ELF in a sealed memfd: denied/unsupported exec never retries by path. */
    sys6(__NR_execveat,image,(long)"",(long)argv,(long)env,EMPTY_PATH,0);
    child_exit(125);
}
/* ONLY a single-threaded freestanding, separately approved guest bootstrap may
 * call this. Not a library for Go/CGO/threads, daemon, launcher API or authority. */
int native_parent_vm_test(void) {
    struct statx output={0},gate_info={0};
    int image=-1,pidfd=-1,report[2]={-1,-1},birth[2]={-1,-1},gate[2]={-1,-1};
    struct clone_args args={0};
    long deadline=now_ms(),pid=-1;int success=0;unsigned char byte=0,bytes[MAX_REPORT];unsigned long length=0;
    __asm__ volatile("cld" ::: "cc");
    if (deadline<0) return 130;
    deadline+=TOTAL_MS;
    for (int fd=0;fd<3;++fd) if (sys6(__NR_fcntl,fd,F_GETFD,0,0,0,0)<0) return 131;
    if (!pipe_role(OUTER_REPORT,O_WRONLY,&output) || !pipe_role(OUTER_GATE,O_RDONLY,&gate_info) || (output.stx_ino==gate_info.stx_ino && output.stx_dev_major==gate_info.stx_dev_major && output.stx_dev_minor==gate_info.stx_dev_minor) || !target_is_empty_domain(deadline)) return 132;
    image=sealed_image(deadline);
    if (image<0 || !new_pipe(report) || !new_pipe(birth) || !new_pipe(gate)) goto done;
    args.flags=CLONE_INTO_CGROUP | CLONE_PIDFD;
    args.pidfd=(unsigned long)&pidfd;args.exit_signal=17;args.cgroup=TARGET_CGROUP;
    /* Exactly ONE clone attempt; even EINTR/unknown errors do not retry. */
    if (!before(deadline)) goto done;
    pid=sys6(__NR_clone3,(long)&args,sizeof(args),0,0,0,0);
    if (pid==0) child_path(image,report,birth,gate);
    if (pid<0 || pidfd<0) goto done;
    close_fd(report[1]);report[1]=-1;close_fd(birth[0]);birth[0]=-1;close_fd(gate[0]);gate[0]=-1;
    if (!read_byte(report[0],&byte,deadline) || byte!='B' || !child_still_waiting(pidfd) || !target_has_only_child((unsigned long)pid,deadline) || !child_still_waiting(pidfd)) goto done;
    if (!phase("phase=pre-exec-observed",(unsigned long)pid,deadline) || !read_byte(OUTER_GATE,&byte,deadline) || byte!=0xa5 || !write_bytes(birth[1],&byte,1,deadline)) goto done;
    close_fd(birth[1]);birth[1]=-1;
    if (!witness_report(report[0],(unsigned long)pid,bytes,&length,deadline) || !child_still_waiting(pidfd) || !target_has_only_child((unsigned long)pid,deadline) || !child_still_waiting(pidfd) || !phase("phase=post-entry-observed",(unsigned long)pid,deadline) || !write_bytes(OUTER_REPORT,bytes,length,deadline) || !read_byte(OUTER_GATE,&byte,deadline) || byte!=0xa5 || !write_bytes(gate[1],&byte,1,deadline)) goto done;
    close_fd(gate[1]);gate[1]=-1;
    success=wait_child(pidfd,(unsigned long)pid,deadline);
done:
    /* Close only private gate writers; fixed child naturally EOFs/times out.
     * NEVER signal a child, launch again, delete cgroup or claim absence/reuse. */
    close_fd(birth[1]);birth[1]=-1;close_fd(gate[1]);gate[1]=-1;
    if (!success && pid>0 && pidfd>=0) (void)wait_child(pidfd,(unsigned long)pid,deadline);
    close_fd(image);close_fd(pidfd);close_fd(report[0]);close_fd(report[1]);close_fd(birth[0]);close_fd(gate[0]);
    return success ? 0 : 133;
}
