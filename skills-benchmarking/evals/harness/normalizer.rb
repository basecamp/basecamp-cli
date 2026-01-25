# frozen_string_literal: true

require "uri"
require "json"

module Evals
  module Normalizer
    module_function

    # Normalize a path (strip slashes, extract from full URL)
    # Returns [normalized_path, normalized_query]
    def normalize_url(url_or_path)
      url_or_path = url_or_path.to_s.strip

      # Parse full URLs
      if url_or_path.start_with?("http://", "https://")
        uri = URI.parse(url_or_path)
        path = uri.path
        query_string = uri.query
      elsif url_or_path.include?("?")
        path, query_string = url_or_path.split("?", 2)
      else
        path = url_or_path
        query_string = nil
      end

      normalized_path = normalize_path(path)
      normalized_query = query_string ? parse_query_string(query_string) : {}

      [normalized_path, normalized_query]
    end

    # Strip leading/trailing slashes (case-sensitive)
    def normalize_path(path)
      path.to_s.gsub(%r{^/+|/+$}, "")
    end

    # Parse query string, normalizing repeated keys to arrays
    def parse_query_string(query_string)
      return {} if query_string.nil? || query_string.empty?

      params = {}

      query_string.split("&").each do |pair|
        key, value = pair.split("=", 2)
        key = URI.decode_www_form_component(key.to_s)
        value = URI.decode_www_form_component(value.to_s)

        # Handle bracket notation: type[] -> type (stored as array)
        array_key = key.end_with?("[]")
        base_key = array_key ? key.chomp("[]") : key

        if params.key?(base_key)
          # Convert to array if not already
          params[base_key] = Array(params[base_key])
          params[base_key] << value
        elsif array_key
          params[base_key] = [value]
        else
          params[base_key] = value
        end
      end

      # Sort array values for deterministic matching
      params.each do |key, value|
        params[key] = value.sort if value.is_a?(Array)
      end

      # Sort by key for deterministic comparison
      params.sort.to_h
    end

    # Normalize query hash from YAML (coerce values to strings, handle arrays)
    def normalize_query(query)
      return {} if query.nil?

      result = {}
      query.each do |key, value|
        # Strip bracket notation from key
        base_key = key.to_s.chomp("[]")

        if value.is_a?(Array)
          result[base_key] = value.map(&:to_s).sort
        else
          result[base_key] = value.to_s
        end
      end

      result.sort.to_h
    end

    # Check if two normalized queries match
    def queries_match?(fixture_query, request_query)
      normalize_query(fixture_query) == normalize_query(request_query)
    end

    # Serialize body for comparison (compact JSON, sorted keys)
    def serialize_body(body)
      return "" if body.nil?
      JSON.generate(deep_sort_keys(body))
    end

    # Check if request body matches fixture body (structural equality)
    def bodies_match?(fixture_body, request_body)
      return true if fixture_body.nil? # No body constraint

      serialize_body(fixture_body) == serialize_body(request_body)
    end

    # Check if body contains substring (for body_contains assertions)
    def body_contains?(body, substring)
      serialize_body(body).include?(substring)
    end

    private

    def self.deep_sort_keys(obj)
      case obj
      when Hash
        obj.sort.to_h.transform_values { |v| deep_sort_keys(v) }
      when Array
        obj.map { |v| deep_sort_keys(v) }
      else
        obj
      end
    end
  end
end
