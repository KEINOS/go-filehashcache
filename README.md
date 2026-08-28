# go-filehashcache

`go-filehashcache` provides the `fhc` Go package. The package calculates fast
change-detection hashes for regular files and recursive directory trees. It
stores reusable content hashes on the file-system objects.

The cache uses these metadata mechanisms:

| Platform | Mechanism |
| --- | --- |
| macOS | Extended attributes |
| Linux | The `user` extended-attribute namespace |
| Windows | NTFS alternate data streams |

The package returns a valid hash with the `UNCACHED` status when the file
system does not support the applicable mechanism.

## Install

```sh
go get github.com/KEINOS/go-filehashcache/fhc
```

## Use

```go
result, err := fhc.GetFileHashWithCache(path)
if err != nil {
    return err
}

fmt.Println(result.Hash, result.Status)
```

The function accepts a regular file or a directory. It does not follow symbolic
links. A directory hash includes all descendant file hashes, directory names,
empty directories, and entry counts.

See [`_examples/basic`](_examples/basic) for a complete program. The example is
not a supported command.

## Cache states

- `HIT`: All required cache records were valid.
- `MISS`: The package calculated and stored new cache data.
- `UNCACHED`: The hash is valid, but some cache data could not be stored.

## Limits

This package detects normal file changes. It is not a cryptographic integrity
control. A program can change content and restore the prior size and
modification time. The package cannot detect that operation from a cached
record.

Metadata can be lost when a file moves to an incompatible file system or passes
through a tool that does not preserve extended attributes or alternate data
streams. Cloud synchronization services can also remove or rewrite metadata.

Docker validates Linux behavior only. GitHub Actions uses native macOS, Linux,
and Windows runners for platform-specific validation.

## Development

```sh
make check
make fuzz
make bench
docker build --target test .
```

The module requires Go 1.27.
