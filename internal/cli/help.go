package cli

const checkHelp = `For each ruleset: validates it against the schema, loads it (inheritance, override policies,
the static checks of the specification), reports YAML and numbers that are not portable, checks
OpenAPI bindings, and runs its golden tests. Without arguments it checks every ruleset of the
project (rules.dir of rcas.yaml); a directory argument means every *.ruleset.* file under it.

Exit status 1 when any file has a problem or a failing test. --json prints one report per file.
`

const compileHelp = `Compiles a ruleset into a bundle: plain JSON, checksummed, that every runtime loads without
YAML or schema validation. With --all, compiles every ruleset of the project into output.dir of
rcas.yaml (or -o), as <id>.bundle.json, plus <id>.<channel>.manifest.json for each channel in
output.manifests. Bundles hold server-only rules: deploy them to backends, never to browsers.
`

const completionHelp = `Prints a completion script. Install it:

  bash        {program} completion bash > ~/.local/share/bash-completion/completions/{program}
  zsh         {program} completion zsh > "${fpath[1]}/_{program}"
  fish        {program} completion fish > ~/.config/fish/completions/{program}.fish
  powershell  {program} completion powershell | Out-String | Invoke-Expression   (add it to $PROFILE)
`
