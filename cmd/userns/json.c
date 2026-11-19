/* SPDX-License-Identifier: Apache-2.0
 * A bounded parser for this launcher's one schema, written for this project.
 * It accepts JSON strings including UTF-8 and surrogate-pair escapes. */
#include "launcher.h"
#include <errno.h>
#include <limits.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

struct parser { const unsigned char *p, *end; };
static void space(struct parser *p) {
    while (p->p < p->end && (*p->p==' ' || *p->p=='\t' || *p->p=='\r' || *p->p=='\n')) ++p->p;
}
static void take(struct parser *p, unsigned char c) {
    space(p);
    if (p->p == p->end || *p->p++ != c) fail("invalid launcher JSON");
}
static unsigned hex4(struct parser *p) {
    unsigned n = 0;
    for (unsigned i = 0; i < 4; ++i) {
        if (p->p == p->end) fail("truncated JSON escape");
        unsigned c = *p->p++, d;
        if (c >= '0' && c <= '9') d = c-'0';
        else if (c >= 'a' && c <= 'f') d = c-'a'+10;
        else if (c >= 'A' && c <= 'F') d = c-'A'+10;
        else fail("invalid JSON escape");
        n = n*16+d;
    }
    return n;
}
static size_t encode(unsigned char *out, unsigned n) {
    if (!n || n < 32 || n == 127) fail("control character in launcher path");
    if (n < 128) { out[0] = (unsigned char)n; return 1; }
    if (n < 2048) { out[0]=0xc0|(n>>6); out[1]=0x80|(n&63); return 2; }
    if (n < 65536) { out[0]=0xe0|(n>>12); out[1]=0x80|((n>>6)&63); out[2]=0x80|(n&63); return 3; }
    out[0]=0xf0|(n>>18); out[1]=0x80|((n>>12)&63); out[2]=0x80|((n>>6)&63); out[3]=0x80|(n&63); return 4;
}
static void append_codepoint(unsigned char *out,size_t *used,size_t capacity,unsigned value) {
    unsigned char bytes[4];size_t length=encode(bytes,value);
    if (*used+length>=capacity) fail("launcher path exceeds PATH_MAX");
    memcpy(out+*used,bytes,length);*used+=length;
}
static char *string(struct parser *p) {
    take(p, '"');
    const unsigned char *end=p->p;
    while (end<p->end && *end!='"') {if (*end=='\\' && end+1<p->end) ++end;++end;}
    if (end==p->end) fail("unterminated launcher string");
    size_t capacity=(size_t)(end-p->p)+1, n=0;
    if (capacity>4096) capacity=4096;
    unsigned char *out=malloc(capacity);
    if (!out) fail("allocate launcher string");
    while (p->p < p->end) {
        unsigned c=*p->p++;
        if (c=='"') { out[n]=0; return (char *)out; }
        if (c=='\\') {
            if (p->p==p->end) fail("truncated JSON string");
            c=*p->p++;
            if (c=='u') {
                c=hex4(p);
                if (c>=0xd800 && c<=0xdbff) {
                    if (p->end-p->p<2 || p->p[0]!='\\' || p->p[1]!='u') fail("unpaired JSON surrogate");
                    p->p+=2; unsigned lo=hex4(p);
                    if (lo<0xdc00 || lo>0xdfff) fail("unpaired JSON surrogate");
                    c=0x10000+((c-0xd800)<<10)+(lo-0xdc00);
                } else if (c>=0xdc00 && c<=0xdfff) fail("unpaired JSON surrogate");
                append_codepoint(out,&n,capacity,c);continue;
            }
            if (c!='"' && c!='\\' && c!='/') fail("control escape in launcher string");
        } else if (c>=128) {
            unsigned length, value, minimum;
            if (c>=0xc2 && c<=0xdf) { length=2;value=c&31;minimum=128; }
            else if (c>=0xe0 && c<=0xef) { length=3;value=c&15;minimum=2048; }
            else if (c>=0xf0 && c<=0xf4) { length=4;value=c&7;minimum=65536; }
            else fail("invalid UTF-8 in launcher request");
            for (unsigned i=1;i<length;++i) {
                if (p->p==p->end || (*p->p&0xc0)!=0x80) fail("invalid UTF-8 continuation");
                value=(value<<6)|(*p->p++&63);
            }
            if (value<minimum || value>0x10ffff || (value>=0xd800 && value<=0xdfff)) fail("non-canonical UTF-8");
            append_codepoint(out,&n,capacity,value);continue;
        }
        append_codepoint(out,&n,capacity,c);
    }
    fail("unterminated launcher string");
}
static unsigned number(struct parser *p) {
    space(p); unsigned n=0;
    if (p->p==p->end || *p->p<'0' || *p->p>'9') fail("launcher number must be an unsigned integer");
    bool zero=*p->p=='0'; unsigned digits=0;
    while (p->p<p->end && *p->p>='0' && *p->p<='9') {
        unsigned d=*p->p++-'0';
        if ((zero && digits) || n>((unsigned)INT_MAX-d)/10) fail("non-canonical launcher number");
        n=n*10+d;++digits;
    }
    return n;
}
static bool boolean(struct parser *p) {
    space(p);
    if (p->end-p->p>=4 && !memcmp(p->p,"true",4)) {p->p+=4;return true;}
    if (p->end-p->p>=5 && !memcmp(p->p,"false",5)) {p->p+=5;return false;}
    fail("launcher field must be a Boolean");
}
static bool another(struct parser *p, unsigned char close) {
    space(p);
    if (p->p<p->end && *p->p==close) {++p->p;return false;}
    take(p,',');return true;
}
static struct mount_request mount_record(struct parser *p) {
    struct mount_request m={0}; unsigned seen=0;
    take(p,'{');
    do {
        char *key=string(p);take(p,':');unsigned bit;
        if (!strcmp(key,"fd")) {bit=1;m.fd=(int)number(p);}
        else if (!strcmp(key,"guest")) {bit=2;m.guest=string(p);}
        else if (!strcmp(key,"write")) {bit=4;m.write=boolean(p);}
        else if (!strcmp(key,"stage_fd")) {bit=8;m.stage_fd=(int)number(p);}
        else if (!strcmp(key,"stage_name")) {bit=16;m.stage_name=string(p);}
        else fail("unknown launcher mount field");
        free(key);if (seen&bit) fail("duplicate launcher mount field");seen|=bit;
    } while (another(p,'}'));
    m.stage_fields=seen&24;
    if ((seen&7)!=7 || m.fd<6) fail("missing launcher mount field or reserved descriptor");
    return m;
}
static void mounts(struct parser *p, struct request *r) {
    take(p,'[');space(p);
    if (p->p<p->end && *p->p==']') {++p->p;return;}
    do {
        struct mount_request m=mount_record(p);
        if (r->count==r->capacity) {
            size_t cap=r->capacity ? r->capacity*2 : 16;
            void *next=realloc(r->mounts,cap*sizeof(m));
            if (!next) fail("allocate launcher mounts");
            r->mounts=next;r->capacity=cap;
        }
        r->mounts[r->count++]=m;
    } while (another(p,']'));
}
void read_request(struct request *r) {
    unsigned char *data=malloc(REQUEST_LIMIT+1);size_t n=0;
    if (!data) fail("allocate bounded request");
    while (n<REQUEST_LIMIT+1) {
        ssize_t got=read(3,data+n,REQUEST_LIMIT+1-n);
        if (got<0 && errno==EINTR) continue;
        if (got<0) fail("read request on fd 3");
        if (!got) break;n+=(size_t)got;
    }
    if (n>REQUEST_LIMIT) fail("launcher request exceeds 4 MiB");
    close(3);
    struct parser p={data,data+n};unsigned seen=0;
    take(&p,'{');
    do {
        char *key=string(&p);take(&p,':');unsigned bit;
        if (!strcmp(key,"v")) {bit=1;r->version=number(&p);}
        else if (!strcmp(key,"probe")) {bit=2;r->probe=boolean(&p);}
        else if (!strcmp(key,"mounts")) {bit=4;mounts(&p,r);}
        else if (!strcmp(key,"dns")) {bit=8;r->dns=boolean(&p);}
        else fail("unknown launcher request field");
        free(key);if (seen&bit) fail("duplicate launcher request field");seen|=bit;
    } while (another(&p,'}'));
    space(&p);
    if (p.p!=p.end || seen!=15 || (r->version!=1 && r->version!=2) || (r->probe && (r->count || r->dns))) fail("unsupported or incomplete launcher request");
    for (size_t i=0;i<r->count;++i) {
        struct mount_request *mount=&r->mounts[i];
        if ((r->version==1 && mount->stage_fields) || (r->version==2 && mount->stage_fields!=24)) fail("incomplete versioned stage fields");
    }
    free(data);
}
