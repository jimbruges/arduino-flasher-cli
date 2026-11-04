#!/usr/bin/env python3
# SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
# SPDX-License-Identifier: GPL-3.0-or-later
"""Compile and exercise the exact patched C digest parser, without USB access."""
import pathlib
import shlex
import subprocess
import sys
import tempfile

source = pathlib.Path(sys.argv[1]).read_text()
generic = source[source.index('static int firehose_generic_parser('):source.index('static int firehose_read(')]
digest = source[source.index('struct storage_digest {'):source.index('static int firehose_issue_read(')]
program = r'''
#include <assert.h>
#include <stdbool.h>
#include <errno.h>
#include <string.h>
#include <libxml/tree.h>
#ifndef __unused
#define __unused __attribute__((unused))
#endif
#define FIREHOSE_ACK 0
#define FIREHOSE_NAK 1
#define ux_log(...) ((void)0)
''' + generic + digest + r'''
static int parse(const char *tag, const char *value, struct storage_digest *digest) {
    bool raw = false;
    xmlNode *node = xmlNewNode(NULL, (xmlChar *)tag);
    xmlNewProp(node, (xmlChar *)"value", (xmlChar *)value);
    int result = firehose_digest_parser(node, digest, &raw);
    xmlFreeNode(node);
    return result;
}
int main(void) {
    const char *valid = "Digest 0123456789abcdef0123456789ABCDEF0123456789abcdef0123456789ABCDEF";
    struct storage_digest d = {0};
    assert(parse("log", valid, &d) == -EAGAIN);
    assert(strlen(d.hex) == 64 && !d.invalid);
    assert(parse("response", "ACK", &d) == FIREHOSE_ACK);
    assert(parse("response", "NAK", &d) == FIREHOSE_NAK);
    parse("log", valid, &d); assert(d.invalid); /* Duplicate digest. */
    const char *bad[] = {"Digest ", "Digest abc", "Digest z123456789abcdef0123456789ABCDEF0123456789abcdef0123456789ABCDEF", "Digest 0123456789abcdef0123456789ABCDEF0123456789abcdef0123456789ABCDEF0"};
    for (unsigned i=0; i<sizeof(bad)/sizeof(bad[0]); i++) {
        struct storage_digest invalid = {0}; parse("log", bad[i], &invalid);
        assert(invalid.invalid && !invalid.hex[0]);
    }
    struct storage_digest missing = {0};
    parse("log", "unrelated log", &missing);
    assert(!missing.hex[0]);
    assert(parse("response", "ACK", &missing) == FIREHOSE_ACK);
    /* ACK without a digest must still be rejected by the caller. */
    return 0;
}
'''
with tempfile.TemporaryDirectory() as work:
    path = pathlib.Path(work)
    (path / 'parser.c').write_text(program)
    flags = shlex.split(subprocess.check_output(['pkg-config', '--cflags', '--libs', 'libxml-2.0'], text=True))
    subprocess.run(['cc', '-Wall', '-Wextra', str(path / 'parser.c'), '-o', str(path / 'parser'), *flags], check=True)
    subprocess.run([str(path / 'parser')], check=True)
print('Storage digest parser tests passed')
