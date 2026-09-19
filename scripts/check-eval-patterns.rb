#!/usr/bin/env ruby
# frozen_string_literal: true

# Compile every pattern in every skill-eval case under the engine that reads
# them. The runner (skill-evals/run) is Ruby, so its patterns are Onigmo, not
# RE2 and not PCRE; a pattern that Go would reject may be fine here and a
# pattern Onigmo rejects raises RegexpError at eval time — where nothing
# catches it, because CI's Skill Evals job cannot run a case without
# ANTHROPIC_API_KEY. A malformed pattern currently lands green.
#
# This does not run the evals. It compiles what they are made of, which is the
# part that was landing unnoticed.

require "yaml"

root = File.expand_path("..", __dir__)
runner = File.join(root, "skill-evals", "run")
cases = Dir.glob(File.join(root, "skill-evals", "cases", "**", "*.yml")).sort

# Where a pattern lives in a case file: a list of strings, or a list of hashes
# with the pattern under one key. Mirrors the six Regexp.new sites in the
# runner.
STRING_LISTS = %w[accept reject accept_response reject_response].freeze
MATCH_LISTS = { "mocks" => "match", "expect_sequence" => "match" }.freeze
MODELLED_SITES = STRING_LISTS.length + MATCH_LISTS.length

errors = []
patterns = 0

# A checker that has fallen behind the runner reports success over the keys it
# still knows. The runner compiles one pattern kind per Regexp.new; if that
# count moves, a pattern kind was added or removed and this list has to move
# with it.
sites = File.read(runner).scan(/Regexp\.new\(/).length
if sites != MODELLED_SITES
  errors << "#{runner}: the runner compiles #{sites} pattern kinds, this checker models " \
            "#{MODELLED_SITES} (#{(STRING_LISTS + MATCH_LISTS.keys).join(", ")}) — update both together"
end

cases.each do |path|
  rel = path.delete_prefix("#{root}/")
  doc = begin
    YAML.safe_load_file(path)
  rescue Psych::Exception => e
    errors << "#{rel}: unparseable YAML: #{e.message}"
    next
  end
  unless doc.is_a?(Hash)
    errors << "#{rel}: expected a mapping at the top level"
    next
  end

  # Shape before content. Normalizing a malformed case into something
  # compilable is how a checker reports success over input it never really
  # examined: a scalar `accept:` coerced to a one-item list compiles fine here
  # and raises NoMethodError in the runner, which calls .each on it, and a
  # string where a mock belongs compiles fine here and raises TypeError there,
  # where the entry is indexed as a mapping (Copilot on #768).
  found = []
  STRING_LISTS.each do |key|
    list = doc[key]
    next if list.nil? # the runner reads these as `(c[key] || [])`
    unless list.is_a?(Array)
      errors << "#{rel}: #{key}: expected a list of patterns, got #{list.class} — the runner calls .each on it"
      next
    end
    list.each_with_index { |pat, i| found << ["#{key}[#{i}]", pat] }
  end
  MATCH_LISTS.each do |key, field|
    list = doc[key]
    next if list.nil?
    unless list.is_a?(Array)
      errors << "#{rel}: #{key}: expected a list of entries, got #{list.class} — the runner iterates it"
      next
    end
    list.each_with_index do |entry, i|
      unless entry.is_a?(Hash)
        errors << "#{rel}: #{key}[#{i}]: expected a mapping with a #{field}: key, got #{entry.class} — the runner indexes it as one"
        next
      end
      found << ["#{key}[#{i}].#{field}", entry[field]]
    end
  end

  found.each do |key, pat|
    unless pat.is_a?(String)
      errors << "#{rel}: #{key}: expected a pattern string, got #{pat.class}"
      next
    end
    patterns += 1
    begin
      Regexp.new(pat)
    rescue RegexpError => e
      errors << "#{rel}: #{key}: /#{pat}/ does not compile: #{e.message}"
    end
  end
end

# A check that compiled nothing passes for the wrong reason. Both floors are
# the assertion, not setup for it.
errors << "no case files found under skill-evals/cases" if cases.empty?
errors << "found #{cases.length} case file(s) and no patterns at all" if patterns.zero?

if errors.empty?
  puts "eval patterns: #{patterns} compiled under Onigmo across #{cases.length} case files"
  exit 0
end

warn "eval patterns: #{errors.length} problem(s)"
errors.each { |e| warn "  #{e}" }
exit 1
