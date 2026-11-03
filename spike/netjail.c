// Fail-closed network jail: runs a command with a seccomp user-notif filter that
// only lets connect/sendto/sendmsg/sendmmsg through to loopback or AF_UNIX peers.
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <linux/filter.h>
#include <linux/seccomp.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/prctl.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <unistd.h>

static int allowed(int pid, unsigned long addr, unsigned long len, char *why) {
  if (addr == 0) return 1; // connected socket send
  unsigned char buf[128]; if (len > sizeof buf) len = sizeof buf;
  char p[64]; snprintf(p, sizeof p, "/proc/%d/mem", pid);
  int fd = open(p, O_RDONLY); if (fd < 0) return 0;
  ssize_t n = pread(fd, buf, len, addr); close(fd);
  if (n < 2) return 0;
  struct sockaddr *sa = (void *)buf;
  if (sa->sa_family == AF_UNIX) return 1;
  if (sa->sa_family == AF_INET && n >= (ssize_t)sizeof(struct sockaddr_in)) {
    struct sockaddr_in *s = (void *)buf;
    if ((ntohl(s->sin_addr.s_addr) >> 24) == 127) return 1;
    inet_ntop(AF_INET, &s->sin_addr, why, 64); return 0;
  }
  if (sa->sa_family == AF_INET6 && n >= (ssize_t)sizeof(struct sockaddr_in6)) {
    struct sockaddr_in6 *s = (void *)buf;
    if (IN6_IS_ADDR_LOOPBACK(&s->sin6_addr)) return 1;
    inet_ntop(AF_INET6, &s->sin6_addr, why, 64); return 0;
  }
  snprintf(why, 64, "family-%d", sa->sa_family); return 0;
}

int main(int argc, char **argv) {
  int nrs[] = {__NR_connect, __NR_sendto, __NR_sendmsg, __NR_sendmmsg};
  struct sock_filter f[32]; int k = 0;
  f[k++] = (struct sock_filter)BPF_STMT(BPF_LD | BPF_W | BPF_ABS, offsetof(struct seccomp_data, nr));
  for (int i = 0; i < 4; i++) {
    f[k++] = (struct sock_filter)BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, nrs[i], 0, 1);
    f[k++] = (struct sock_filter)BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_USER_NOTIF);
  }
  f[k++] = (struct sock_filter)BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW);
  struct sock_fprog prog = {k, f};
  int sv[2]; socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
  pid_t child = fork();
  if (child == 0) {
    close(sv[0]);
    prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0);
    int nfd = syscall(SYS_seccomp, SECCOMP_SET_MODE_FILTER, SECCOMP_FILTER_FLAG_NEW_LISTENER, &prog);
    if (nfd < 0) { perror("seccomp"); _exit(127); }
    write(sv[1], &nfd, 0); // placeholder
    // pass fd via SCM_RIGHTS
    char c = 0; struct iovec iov = {&c, 1}; char cb[CMSG_SPACE(sizeof(int))];
    struct msghdr m = {0}; m.msg_iov = &iov; m.msg_iovlen = 1; m.msg_control = cb; m.msg_controllen = sizeof cb;
    struct cmsghdr *cm = CMSG_FIRSTHDR(&m); cm->cmsg_level = SOL_SOCKET; cm->cmsg_type = SCM_RIGHTS;
    cm->cmsg_len = CMSG_LEN(sizeof(int)); memcpy(CMSG_DATA(cm), &nfd, sizeof(int));
    sendmsg(sv[1], &m, 0); close(nfd); close(sv[1]);
    execvp(argv[1], argv + 1); perror("exec"); _exit(127);
  }
  close(sv[1]);
  char c; struct iovec iov = {&c, 1}; char cb[CMSG_SPACE(sizeof(int))];
  struct msghdr m = {0}; m.msg_iov = &iov; m.msg_iovlen = 1; m.msg_control = cb; m.msg_controllen = sizeof cb;
  if (recvmsg(sv[0], &m, 0) < 0) { perror("recvmsg"); return 1; }
  int nfd; memcpy(&nfd, CMSG_DATA(CMSG_FIRSTHDR(&m)), sizeof nfd);
  FILE *log = fopen(getenv("JAIL_LOG") ? getenv("JAIL_LOG") : "/dev/stderr", "a");
  pid_t w = fork();
  if (w == 0) { // supervisor
    struct seccomp_notif_sizes sz; syscall(SYS_seccomp, SECCOMP_GET_NOTIF_SIZES, 0, &sz);
    struct seccomp_notif *req = malloc(sz.seccomp_notif);
    struct seccomp_notif_resp *resp = malloc(sz.seccomp_notif_resp);
    for (;;) {
      memset(req, 0, sz.seccomp_notif);
      if (ioctl(nfd, SECCOMP_IOCTL_NOTIF_RECV, req) < 0) { if (errno == EINTR) continue; break; }
      unsigned long a, l; int nr = req->data.nr;
      if (nr == __NR_connect) { a = req->data.args[1]; l = req->data.args[2]; }
      else if (nr == __NR_sendto) { a = req->data.args[4]; l = req->data.args[5]; }
      else { // sendmsg/sendmmsg: msghdr at args[1]; msg_name,msg_namelen first
        char p[64]; snprintf(p, sizeof p, "/proc/%u/mem", req->pid);
        int fd = open(p, O_RDONLY); unsigned long mh[2] = {0, 0};
        if (fd >= 0) { pread(fd, mh, sizeof mh, req->data.args[1]); close(fd); }
        a = mh[0]; l = 0; if (a) { l = 28; }
      }
      char why[64] = "?"; int ok = allowed(req->pid, a, l, why);
      memset(resp, 0, sz.seccomp_notif_resp); resp->id = req->id;
      if (ok) resp->flags = SECCOMP_USER_NOTIF_FLAG_CONTINUE; else { resp->error = -ECONNREFUSED; fprintf(log, "JAIL BLOCKED nr=%d dst=%s\n", nr, why); fflush(log); }
      ioctl(nfd, SECCOMP_IOCTL_NOTIF_SEND, resp);
    }
    _exit(0);
  }
  int st; waitpid(child, &st, 0); kill(w, 9); return WEXITSTATUS(st);
}
