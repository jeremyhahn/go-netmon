### Changed

- **release**: A release is published as soon as its release pull request merges and CI passes on `main`; the tag, archives, images and GitHub release follow without a manual step.
- **release**: Before 1.0 every release, `-alpha` and `-rc.N` included, is GitHub's Latest release and moves the images' `X.Y` and `latest` tags, so `docker pull ghcr.io/jeremyhahn/go-netmon:latest` and the Releases page show the current release. From 1.0 on only a plain `vX.Y.Z` does.
