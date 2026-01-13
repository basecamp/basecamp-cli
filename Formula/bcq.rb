# typed: false
# frozen_string_literal: true

class Bcq < Formula
  desc "Agent-first CLI for Basecamp API interaction"
  homepage "https://github.com/basecamp/bcq"
  url "https://github.com/basecamp/bcq/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "PLACEHOLDER_SHA256"
  license "MIT"
  head "https://github.com/basecamp/bcq.git", branch: "main"

  depends_on "bash"
  depends_on "curl"
  depends_on "jq"

  def install
    # Install library files to libexec
    libexec.install "lib"
    libexec.install "bin/bcq"

    # Patch the script to respect BCQ_ROOT if already set
    inreplace libexec/"bcq",
      'BCQ_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"',
      'BCQ_ROOT="${BCQ_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"'

    # Create wrapper script that sets BCQ_ROOT
    (bin/"bcq").write <<~EOS
      #!/bin/bash
      export BCQ_ROOT="#{libexec}"
      exec "#{Formula["bash"].opt_bin}/bash" "#{libexec}/bcq" "$@"
    EOS

    # Install completions
    bash_completion.install "completions/bcq.bash" => "bcq"
    zsh_completion.install "completions/bcq.zsh" => "_bcq"
  end

  def caveats
    <<~EOS
      bcq requires authentication before use:
        bcq auth login

      For project-specific defaults, create .basecamp/config.json in your repo:
        bcq config init

      Quick start:
        bcq projects        # List all projects
        bcq todos           # Your assigned todos
        bcq search "query"  # Search everything
    EOS
  end

  test do
    # Test that bcq runs and shows help
    assert_match "bcq", shell_output("#{bin}/bcq --help")
    assert_match "projects", shell_output("#{bin}/bcq --help")
  end
end
