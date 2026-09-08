# Construct Developer API

The developer portal backend: publishers, spaces, publishes, CLI tokens, bundle signing and the review flow behind developer.construct.space. Go, MySQL, R2 through `storage-api`.

Part of [Construct](https://github.com/construct-space), the platform behind construct.space, published as it ran in September 2026. The organisation README maps the other services.

## Run

```
go run .
```

Copy `.env.sample` to `.env` and fill in the values; secrets are marked `change-me`.
A `Dockerfile` and a `captain-definition` are included: the service ran on CapRover.

## License

MIT, see `LICENSE`.