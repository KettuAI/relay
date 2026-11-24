# Test data

`relay-vectors.json` is a verbatim copy of `src/shared/remote/relay-vectors.json` in
[KettuAI/Kettu](https://github.com/KettuAI/Kettu), the source of the protocol's test vectors,
taken at commit `4ab57c032ecb66c32ae570caead47ad3a9d2aab1` (the file last changed in
`cf7ea7ba5237231332125139455efbd9f5824608`). Copy it again from there when it changes; never edit
it here.

This relay's tests use the key, sign-in, paths, close codes, message and drain vectors. The file's
other sections describe what clients read elsewhere and are not used here.

One difference is deliberate: a device's `{"type":"connect"}` without a token is invalid in the
vectors, where a relay requires an account token, but this relay takes it, since its devices
bring none.
