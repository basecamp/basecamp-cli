# frozen_string_literal: true

require_relative "normalizer"

module Evals
  class Assertions
    Result = Struct.new(:passed, :message, :details, keyword_init: true)

    
    def initialize(assertions:, request_log:, max_calls_exceeded: false)
      @assertions = assertions || {}
      @request_log = request_log
      @max_calls_exceeded = max_calls_exceeded
    end

    def check_all
      results = []

      # Check max_calls first (may have already failed via hard stop)
      if @assertions[:max_calls]
        results << check_max_calls(@assertions[:max_calls])
      end

      if @assertions[:required_sequence]
        strict = @assertions[:strict] || false
        results << check_required_sequence(@assertions[:required_sequence], strict: strict)
      end

      if @assertions[:required_any]
        results << check_required_any(@assertions[:required_any])
      end

      if @assertions[:forbidden]
        results.concat(check_forbidden(@assertions[:forbidden]))
      end

      if @assertions[:end_state]
        results.concat(check_end_state(@assertions[:end_state]))
      end

      results
    end

    def passed?
      check_all.all?(&:passed)
    end

    private

    def check_max_calls(limit)
      count = @request_log.size
      # Hard fail if agent attempted to exceed (caught by runner pre-check)
      if @max_calls_exceeded
        Result.new(
          passed: false,
          message: "max_calls exceeded: agent attempted call #{count + 1} (limit: #{limit})",
          details: { limit: limit, actual: count, exceeded: true }
        )
      elsif count > limit
        Result.new(
          passed: false,
          message: "max_calls exceeded: #{count} > #{limit}",
          details: { limit: limit, actual: count }
        )
      else
        Result.new(
          passed: true,
          message: "max_calls: #{count}/#{limit}",
          details: { limit: limit, actual: count }
        )
      end
    end

    def check_required_sequence(sequence, strict: false)
      log_index = 0
      matched = []

      sequence.each_with_index do |step, step_index|
        found = false
        skipped_calls = []

        while log_index < @request_log.size
          entry = @request_log[log_index]
          log_index += 1

          if matches_step?(step, entry)
            # Check occurrence constraint
            if step[:occurrence]
              occurrence = count_occurrences_up_to(step, log_index)
              next unless occurrence == step[:occurrence]
            end

            # Check expect_status
            if step[:expect_status] && entry[:status] != step[:expect_status]
              return Result.new(
                passed: false,
                message: "required_sequence[#{step_index}]: expected status #{step[:expect_status]}, got #{entry[:status]}",
                details: { step: step, entry: entry }
              )
            end

            # strict mode: fail if any calls were skipped between sequence steps
            if strict && !skipped_calls.empty?
              return Result.new(
                passed: false,
                message: "required_sequence[#{step_index}]: strict mode violation - #{skipped_calls.size} unexpected call(s) before this step",
                details: { step: step, skipped: skipped_calls }
              )
            end

            matched << { step_index: step_index, log_index: log_index - 1, entry: entry }
            found = true
            break
          else
            skipped_calls << entry
          end
        end

        unless found
          return Result.new(
            passed: false,
            message: "required_sequence[#{step_index}]: not found (#{step[:method]} #{step[:path]})",
            details: { step: step, matched_so_far: matched }
          )
        end
      end

      Result.new(
        passed: true,
        message: "required_sequence: #{matched.size}/#{sequence.size} calls",
        details: { matched: matched }
      )
    end

    def check_required_any(alternatives)
      found = alternatives.any? do |alt|
        @request_log.any? { |entry| matches_step?(alt, entry) }
      end

      if found
        Result.new(
          passed: true,
          message: "required_any: matched",
          details: { alternatives: alternatives }
        )
      else
        Result.new(
          passed: false,
          message: "required_any: none matched",
          details: { alternatives: alternatives }
        )
      end
    end

    def check_forbidden(forbidden_list)
      forbidden_list.map do |rule|
        max_count = rule[:max_count] || 0
        matches = @request_log.count do |entry|
          matches_step?(rule, entry) &&
            (!rule[:body_contains] || Normalizer.body_contains?(entry[:body], rule[:body_contains]))
        end

        if matches > max_count
          Result.new(
            passed: false,
            message: "forbidden: #{rule[:method]} #{rule[:path]} occurred #{matches}x (max: #{max_count})",
            details: { rule: rule, count: matches }
          )
        else
          Result.new(
            passed: true,
            message: "forbidden: #{rule[:method]} #{rule[:path]} #{matches}/#{max_count}",
            details: { rule: rule, count: matches }
          )
        end
      end
    end

    def check_end_state(conditions)
      conditions.map do |condition|
        matches = @request_log.select do |entry|
          matches_step?(condition, entry) &&
            (!condition[:body_contains] || Normalizer.body_contains?(entry[:body], condition[:body_contains]))
        end

        actual_count = matches.size

        # Support both exact count and min_count
        if condition[:min_count]
          min_count = condition[:min_count]
          if actual_count >= min_count
            Result.new(
              passed: true,
              message: "end_state: #{condition[:method]} #{condition[:path]} count=#{actual_count} (min: #{min_count})",
              details: { condition: condition, matches: matches }
            )
          else
            Result.new(
              passed: false,
              message: "end_state: #{condition[:method]} #{condition[:path]} expected >= #{min_count}, got #{actual_count}",
              details: { condition: condition, matches: matches }
            )
          end
        else
          expected_count = condition[:count]
          if actual_count == expected_count
            Result.new(
              passed: true,
              message: "end_state: #{condition[:method]} #{condition[:path]} count=#{actual_count}",
              details: { condition: condition, matches: matches }
            )
          else
            Result.new(
              passed: false,
              message: "end_state: #{condition[:method]} #{condition[:path]} expected #{expected_count}, got #{actual_count}",
              details: { condition: condition, matches: matches }
            )
          end
        end
      end
    end

    def matches_step?(step, entry)
      return false unless step[:method].to_s.upcase == entry[:method]

      step_path = Normalizer.normalize_path(step[:path])
      return false unless step_path == entry[:path]

      if step[:query]
        step_query = Normalizer.normalize_query(step[:query])
        return false unless Normalizer.queries_match?(step_query, entry[:query])
      end

      true
    end

    def count_occurrences_up_to(step, up_to_index)
      @request_log[0...up_to_index].count { |entry| matches_step?(step, entry) }
    end
  end
end
