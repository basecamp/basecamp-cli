MODEL ?= claude-sonnet-4-20250514
SKILL ?= ../skills/basecamp/SKILL.md

.PHONY: eval eval-syntax eval-save eval-compare

eval:
	./run --model $(MODEL) --skill $(SKILL)

eval-syntax:
	./run --model $(MODEL) --skill $(SKILL) --tag calling-convention

eval-save:
	@test -n "$(NAME)" || (echo "Usage: make eval-save NAME=baseline" && exit 1)
	./run --model $(MODEL) --skill $(SKILL) --save $(NAME)

eval-compare:
	@test -n "$(NAME)" || (echo "Usage: make eval-compare NAME=baseline" && exit 1)
	./run --model $(MODEL) --skill $(SKILL) --compare $(NAME)
