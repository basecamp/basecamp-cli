{ lib, buildGoModule, installShellFiles, stdenv }:

buildGoModule (finalAttrs: {
  pname = "basecamp";
  version = "0.2.3";

  src = lib.cleanSource ./..;

  # To update: set to lib.fakeHash, run `nix build`, use the hash from the error.
  vendorHash = lib.fakeHash;

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
