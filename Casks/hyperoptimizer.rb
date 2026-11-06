# Bootstrapped from published v0.1.0 archives; subsequent updates use GoReleaser.
cask "hyperoptimizer" do
  version "0.1.0"

  on_macos do
    on_arm do
      sha256 "ddc0a71ac642f187c5bf8fdfb977c91343d7e7976395d07d2b8d1d6b580ba95f"
      url "https://github.com/hyperoptimizer/cli/releases/download/v0.1.0/hpo_#{version}_darwin_arm64.tar.gz"
    end
    on_intel do
      sha256 "aab1505467c516701a1a5cde43512ecd7315f59f609af0f505b0319b86c8edfd"
      url "https://github.com/hyperoptimizer/cli/releases/download/v0.1.0/hpo_#{version}_darwin_amd64.tar.gz"
    end
  end
  on_linux do
    on_arm do
      sha256 "fcbbfb234514e9b96f97b6b8271e30ffc6ded912658d09834ff44a79c1733098"
      url "https://github.com/hyperoptimizer/cli/releases/download/v0.1.0/hpo_#{version}_linux_arm64.tar.gz"
    end
    on_intel do
      sha256 "b359eae75e007a93946525d35147c3eeb94c9291474807f5fb53f5d51cc9f260"
      url "https://github.com/hyperoptimizer/cli/releases/download/v0.1.0/hpo_#{version}_linux_amd64.tar.gz"
    end
  end

  name "hyperoptimizer"
  desc "Run hyperparameter optimization experiments from your terminal"
  homepage "https://hyperoptimizer.com/"

  livecheck do
    skip "Auto-generated on release."
  end

  binary "hpo"
  binary "hyperoptimizer"
  generate_completions_from_executable "hpo", "completion"

  preflight_steps do
    on_macos do
      run "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "{{staged_path}}/hpo"]
      run "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "{{staged_path}}/hyperoptimizer"]
    end
  end

  zap :not_required
end
