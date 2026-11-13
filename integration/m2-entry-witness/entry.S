/* Linux x86_64 only. No CRT/loader; argv, env and auxv are deliberately unused. */
.text
.global _start
.type _start,@function
_start:
    cld
    xor %ebp, %ebp
    and $-16, %rsp
    call witness_main
    mov $60, %eax
    mov $117, %edi
    syscall
    ud2
.size _start,.-_start
.section .note.GNU-stack,"",@progbits
