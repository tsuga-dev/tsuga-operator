# Documentation and publishing

This site uses [Material for MkDocs](https://squidfunk.github.io/mkdocs-material/). Its source is `docs/site/`.

## Preview locally

From the repository root, using Python 3.12 or newer:

```sh
make docs-setup
make docs-serve
```

Open `http://127.0.0.1:8000`. Markdown changes reload automatically. Dependencies are isolated in `.venv-docs`; no Go, Kubernetes cluster, or Tsuga credentials are needed for the documentation build.

## Validate changes

```sh
make docs-build
```

This checks generated CRD reference pages for drift, validates downloadable examples, and builds with strict navigation/link/anchor checks. It checks internal documentation links, not live availability of external sites or execution of tutorial commands. The output is written to `site/` and is not committed.

To regenerate field tables after a schema update:

```sh
make docs-reference
```

Do not hand-edit generated reference pages. Update schemas or the behavior notes in `hack/docs.py`. Tutorial code blocks include the downloadable YAML through snippets, so readers see the same examples that CI validates.

## Publish on GitHub Pages

The [documentation workflow](https://github.com/tsuga-dev/tsuga-operator/blob/main/.github/workflows/docs.yml) builds every pull request to `main`. Pushes to `main` also upload and deploy the site using the official Pages actions. Manual dispatch deploys only when run on `main`.

One-time repository setup:

1. In **Settings → Pages → Build and deployment**, select **GitHub Actions** as the source.
2. Ensure the `github-pages` environment allows deployment from `main` and that repository/organization policy permits Pages.
3. Merge the documentation changes to `main`, then watch the **Documentation** workflow.
4. Verify the site at `https://tsuga-dev.github.io/tsuga-operator/`.

The deploy job needs `pages: write` and `id-token: write`; the build job has read-only repository access. No personal access token is required. Follow GitHub's [custom Pages workflow documentation](https://docs.github.com/en/pages/getting-started-with-github-pages/using-custom-workflows-with-github-pages) if organization settings restrict publishing.

For a fork or custom domain, update `site_url`, repository/edit links, and the generated reference source links before publishing.

## Content conventions

Organize the sidebar by feature: Home, Getting started, Kubernetes monitoring, Dashboards, Monitors, and SLOs. Keep shared operational and contributor documentation under Resources.

Home explains what the operator does and when to use it. Getting started covers installation. Each feature section keeps its setup guide, related tasks, and API reference together. Avoid labels that classify readers by experience.

Link to prerequisites instead of repeating them, provide complete examples and verification steps, use placeholders for private values, and distinguish current behavior from planned features. Add each new page to `mkdocs.yml` navigation. Keep the sidebar available on every page and check desktop and narrow-screen layouts when changing the theme.

These docs track `main`. Release-specific publishing can be added when multiple supported release lines need separate documentation; do not label main-branch content as a stable release.
