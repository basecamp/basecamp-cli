{ lib, buildGoModule, go_1_26, installShellFiles, stdenv }:

buildGoModule.override { go = go_1_26; } (finalAttrs: {
  pname = "basecamp";
  # Updated automatically by scripts/update-nix-flake.sh on each release.
  version = "0.7.0";

  src = lib.cleanSource ./..;

  # To update: set to lib.fakeHash, run `nix build`, use the hash from the error.
  vendorHash = "sha256-znZp6z4ZqlYxZ20TIFC5ui+AXLgr7tPqYfCB2tc7Uqg=";

  subPackages = [ "cmd/basecamp" ];

  ldflags = [
    "-s" "-w"
    "-X github.com/basecamp/basecamp-cli/internal/version.Version=${finalAttrs.version}"
  ];

  nativeBuildInputs = [ installShellFiles ];

  postInstall = lib.optionalString
    (stdenv.buildPlatform.canExecute stdenv.hostPlatform) ''
    installShellCompletion --cmd basecamp \
      --bash <($out/bin/basecamp completion bash) \
      --fish <($out/bin/basecamp completion fish) \
      --zsh  <($out/bin/basecamp completion zsh)
  '';

  meta = {
    description = "Command-line interface for Basecamp";
    homepage = "https://github.com/basecamp/basecamp-cli";
    changelog = "https://github.com/basecamp/basecamp-cli/releases/tag/v${finalAttrs.version}";
    license = lib.licenses.mit;
    mainProgram = "basecamp";
  };
})
