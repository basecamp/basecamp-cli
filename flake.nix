{
  description = "Command-line interface for Basecamp";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      supportedSystems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
      nixpkgsFor = forAllSystems (system: import nixpkgs { inherit system; });
    in {
      packages = forAllSystems (system:
        let pkgs = nixpkgsFor.${system};
        in {
          basecamp = pkgs.callPackage ./nix/package.nix { };
          default = self.packages.${system}.basecamp;
        }
      );

      devShells = forAllSystems (system:
        let pkgs = nixpkgsFor.${system};
        in {
          default = pkgs.mkShell {
            packages = with pkgs; [
              actionlint
              bats
              git
              go_1_26
              golangci-lint
              goreleaser
              gnumake
              jq
              ripgrep
              # Keep in sync with .mise.toml and the ruby/setup-ruby pins in
              # .github/workflows/test.yml: make check compiles the skill-eval
              # patterns under Ruby's own regex engine.
              ruby_3_3
              zizmor
            ];
          };
        }
      );
    };
}
