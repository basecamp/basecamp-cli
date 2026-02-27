package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewCheckinsCmd creates the checkins command group.
func NewCheckinsCmd() *cobra.Command {
	var project string
	var questionnaireID string

	cmd := &cobra.Command{
		Use:     "checkins",
		Aliases: []string{"checkin"},
		Short:   "Manage automatic check-ins",
		Long: `Manage automatic check-ins (questionnaires, questions, and answers).

Check-ins are recurring questions that collect answers from team members
on a schedule (e.g., "What did you work on today?").`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to show questionnaire when called without subcommand
			return runCheckinsShow(cmd, project, questionnaireID)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVar(&questionnaireID, "questionnaire", "", "Questionnaire ID (auto-detected)")

	cmd.AddCommand(
		newCheckinsQuestionsCmd(&project, &questionnaireID),
		newCheckinsQuestionCmd(&project),
		newCheckinsAnswersCmd(&project),
		newCheckinsAnswerCmd(&project),
	)

	return cmd
}

func runCheckinsShow(cmd *cobra.Command, project, questionnaireID string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// Get questionnaire ID
	resolvedQuestionnaireID := questionnaireID
	if resolvedQuestionnaireID == "" {
		resolvedQuestionnaireID, err = getQuestionnaireID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	qID, err := strconv.ParseInt(resolvedQuestionnaireID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid questionnaire ID")
	}

	questionnaire, err := app.Account().Checkins().GetQuestionnaire(cmd.Context(), qID)
	if err != nil {
		return convertSDKError(err)
	}

	name := questionnaire.Name
	if name == "" {
		name = "Automatic Check-ins"
	}
	summary := fmt.Sprintf("%s (%d questions)", name, questionnaire.QuestionsCount)

	return app.OK(questionnaire,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "questions",
				Cmd:         fmt.Sprintf("basecamp checkins questions --in %s", resolvedProjectID),
				Description: "View questions",
			},
		),
	)
}

func newCheckinsQuestionsCmd(project, questionnaireID *string) *cobra.Command {
	var limit int
	var page int
	var all bool

	cmd := &cobra.Command{
		Use:   "questions",
		Short: "List check-in questions",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Validate flag combinations
			if all && limit > 0 {
				return output.ErrUsage("--all and --limit are mutually exclusive")
			}
			if page > 0 && (all || limit > 0) {
				return output.ErrUsage("--page cannot be combined with --all or --limit")
			}
			if page > 1 {
				return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Resolve project, with interactive fallback
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get questionnaire ID
			resolvedQuestionnaireID := *questionnaireID
			if resolvedQuestionnaireID == "" {
				resolvedQuestionnaireID, err = getQuestionnaireID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			qID, err := strconv.ParseInt(resolvedQuestionnaireID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid questionnaire ID")
			}

			// Build pagination options
			opts := &basecamp.QuestionListOptions{}
			if all {
				opts.Limit = -1 // SDK treats -1 as "fetch all"
			} else if limit > 0 {
				opts.Limit = limit
			}
			if page > 0 {
				opts.Page = page
			}

			questionsResult, err := app.Account().Checkins().ListQuestions(cmd.Context(), qID, opts)
			if err != nil {
				return convertSDKError(err)
			}
			questions := questionsResult.Questions

			return app.OK(questions,
				output.WithSummary(fmt.Sprintf("%d check-in questions", len(questions))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "question",
						Cmd:         fmt.Sprintf("basecamp checkins question <id> --in %s", resolvedProjectID),
						Description: "View question details",
					},
					output.Breadcrumb{
						Action:      "answers",
						Cmd:         fmt.Sprintf("basecamp checkins answers <question_id> --in %s", resolvedProjectID),
						Description: "View answers",
					},
				),
			)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of questions to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all questions (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	return cmd
}

func newCheckinsQuestionCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "question [id|url]",
		Short: "Show or manage a question",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return output.ErrUsage("Question ID required")
			}
			return runCheckinsQuestionShow(cmd, *project, args[0])
		},
	}

	cmd.AddCommand(
		newCheckinsQuestionShowCmd(project),
		newCheckinsQuestionCreateCmd(project),
		newCheckinsQuestionUpdateCmd(project),
	)

	return cmd
}

func newCheckinsQuestionShowCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show question details",
		Long: `Display details of a check-in question.

You can pass either a question ID or a Basecamp URL:
  basecamp checkins question show 789 --in my-project
  basecamp checkins question show https://3.basecamp.com/123/buckets/456/questionnaires/questions/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheckinsQuestionShow(cmd, *project, args[0])
		},
	}
}

func runCheckinsQuestionShow(cmd *cobra.Command, project, questionIDStr string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Extract ID and project from URL if provided
	questionIDStr, urlProjectID := extractWithProject(questionIDStr)

	// Resolve project - use URL > flag > config, with interactive fallback
	projectID := project
	if projectID == "" && urlProjectID != "" {
		projectID = urlProjectID
	}
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	questionID, err := strconv.ParseInt(questionIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid question ID")
	}

	question, err := app.Account().Checkins().GetQuestion(cmd.Context(), questionID)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("%s (%d answers)", question.Title, question.AnswersCount)

	return app.OK(question,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "answers",
				Cmd:         fmt.Sprintf("basecamp checkins answers %s --in %s", questionIDStr, resolvedProjectID),
				Description: "View answers",
			},
			output.Breadcrumb{
				Action:      "questions",
				Cmd:         fmt.Sprintf("basecamp checkins questions --in %s", resolvedProjectID),
				Description: "View all questions",
			},
		),
	)
}

func newCheckinsQuestionCreateCmd(project *string) *cobra.Command {
	var questionnaireID string
	var title string
	var frequency string
	var timeOfDay string
	var days string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new check-in question",
		Long: `Create a new check-in question.

Frequency options: every_day, every_week, every_other_week, every_month, on_certain_days
Days format: comma-separated (0=Sun, 1=Mon, 2=Tue, 3=Wed, 4=Thu, 5=Fri, 6=Sat)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if title == "" {
				return output.ErrUsage("--title is required")
			}

			// Resolve project, with interactive fallback
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get questionnaire ID
			resolvedQuestionnaireID := questionnaireID
			if resolvedQuestionnaireID == "" {
				resolvedQuestionnaireID, err = getQuestionnaireID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			qID, err := strconv.ParseInt(resolvedQuestionnaireID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid questionnaire ID")
			}

			// Default values
			if frequency == "" {
				frequency = "every_day"
			}
			if days == "" {
				days = "1,2,3,4,5"
			}

			// Parse days into array of ints
			dayParts := strings.Split(days, ",")
			daysArray := make([]int, 0, len(dayParts))
			for _, d := range dayParts {
				d = strings.TrimSpace(d)
				if d != "" {
					dayInt, err := strconv.Atoi(d)
					if err != nil {
						return output.ErrUsage("Invalid day value: " + d)
					}
					daysArray = append(daysArray, dayInt)
				}
			}

			// Parse time of day (default 5:00pm = 17:00)
			hour := 17
			minute := 0
			if timeOfDay != "" {
				hour, minute, err = parseTimeOfDay(timeOfDay)
				if err != nil {
					return output.ErrUsage("Invalid time format: " + timeOfDay)
				}
			}

			req := &basecamp.CreateQuestionRequest{
				Title: title,
				Schedule: &basecamp.QuestionSchedule{
					Frequency: frequency,
					Days:      daysArray,
					Hour:      hour,
					Minute:    minute,
				},
			}

			question, err := app.Account().Checkins().CreateQuestion(cmd.Context(), qID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(question,
				output.WithSummary(fmt.Sprintf("Created question #%d: %s", question.ID, question.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "question",
						Cmd:         fmt.Sprintf("basecamp checkins question %d --in %s", question.ID, resolvedProjectID),
						Description: "View question",
					},
					output.Breadcrumb{
						Action:      "questions",
						Cmd:         fmt.Sprintf("basecamp checkins questions --in %s", resolvedProjectID),
						Description: "View all questions",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&questionnaireID, "questionnaire", "", "Questionnaire ID (auto-detected)")
	cmd.Flags().StringVarP(&title, "title", "t", "", "Question text (required)")
	cmd.Flags().StringVarP(&frequency, "frequency", "f", "", "Schedule frequency (default: every_day)")
	cmd.Flags().StringVar(&timeOfDay, "time", "", "Time to ask (default: 5:00pm)")
	cmd.Flags().StringVarP(&days, "days", "d", "", "Days to ask, comma-separated (default: 1,2,3,4,5)")
	_ = cmd.MarkFlagRequired("title")

	return cmd
}

func newCheckinsQuestionUpdateCmd(project *string) *cobra.Command {
	var title string
	var frequency string
	var timeOfDay string
	var days string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a check-in question",
		Long: `Update a check-in question's title or schedule.

You can pass either a question ID or a Basecamp URL:
  basecamp checkins question update 789 --title "new question" --in my-project
  basecamp checkins question update https://3.basecamp.com/123/buckets/456/questionnaires/questions/789 --title "new question"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			questionIDStr, urlProjectID := extractWithProject(args[0])

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			questionID, err := strconv.ParseInt(questionIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid question ID")
			}

			// Build request
			req := &basecamp.UpdateQuestionRequest{}
			if title != "" {
				req.Title = title
			}

			if frequency != "" || timeOfDay != "" || days != "" {
				schedule := &basecamp.QuestionSchedule{}
				if frequency != "" {
					schedule.Frequency = frequency
				}
				if timeOfDay != "" {
					hour, minute, err := parseTimeOfDay(timeOfDay)
					if err != nil {
						return output.ErrUsage("Invalid time format: " + timeOfDay)
					}
					schedule.Hour = hour
					schedule.Minute = minute
				}
				if days != "" {
					dayParts := strings.Split(days, ",")
					daysArray := make([]int, 0, len(dayParts))
					for _, d := range dayParts {
						d = strings.TrimSpace(d)
						if d != "" {
							dayInt, err := strconv.Atoi(d)
							if err != nil {
								return output.ErrUsage("Invalid day value: " + d)
							}
							daysArray = append(daysArray, dayInt)
						}
					}
					schedule.Days = daysArray
				}
				req.Schedule = schedule
			}

			if req.Title == "" && req.Schedule == nil {
				return output.ErrUsage("at least one of --title, --frequency, --time, or --days is required")
			}

			question, err := app.Account().Checkins().UpdateQuestion(cmd.Context(), questionID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(question,
				output.WithSummary(fmt.Sprintf("Updated question #%s: %s", questionIDStr, question.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "question",
						Cmd:         fmt.Sprintf("basecamp checkins question %s --in %s", questionIDStr, resolvedProjectID),
						Description: "View question",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "New question text")
	cmd.Flags().StringVarP(&frequency, "frequency", "f", "", "New schedule frequency")
	cmd.Flags().StringVar(&timeOfDay, "time", "", "New time to ask")
	cmd.Flags().StringVarP(&days, "days", "d", "", "New days to ask")

	return cmd
}

func newCheckinsAnswersCmd(project *string) *cobra.Command {
	var limit int
	var page int
	var all bool

	cmd := &cobra.Command{
		Use:   "answers <question_id|url>",
		Short: "List answers for a question",
		Long: `List answers for a check-in question.

You can pass either a question ID or a Basecamp URL:
  basecamp checkins answers 789 --in my-project
  basecamp checkins answers https://3.basecamp.com/123/buckets/456/questions/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Validate flag combinations
			if all && limit > 0 {
				return output.ErrUsage("--all and --limit are mutually exclusive")
			}
			if page > 0 && (all || limit > 0) {
				return output.ErrUsage("--page cannot be combined with --all or --limit")
			}
			if page > 1 {
				return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			questionIDStr, urlProjectID := extractWithProject(args[0])

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			questionID, err := strconv.ParseInt(questionIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid question ID")
			}

			// Build pagination options
			opts := &basecamp.AnswerListOptions{}
			if all {
				opts.Limit = -1 // SDK treats -1 as "fetch all"
			} else if limit > 0 {
				opts.Limit = limit
			}
			if page > 0 {
				opts.Page = page
			}

			answersResult, err := app.Account().Checkins().ListAnswers(cmd.Context(), questionID, opts)
			if err != nil {
				return convertSDKError(err)
			}
			answers := answersResult.Answers

			return app.OK(answers,
				output.WithSummary(fmt.Sprintf("%d answers", len(answers))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "answer",
						Cmd:         fmt.Sprintf("basecamp checkins answer <id> --in %s", resolvedProjectID),
						Description: "View answer details",
					},
					output.Breadcrumb{
						Action:      "question",
						Cmd:         fmt.Sprintf("basecamp checkins question %s --in %s", questionIDStr, resolvedProjectID),
						Description: "View question",
					},
				),
			)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of answers to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all answers (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	return cmd
}

func newCheckinsAnswerCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "answer [id|url]",
		Short: "Show or manage an answer",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return output.ErrUsage("Answer ID required")
			}
			return runCheckinsAnswerShow(cmd, *project, args[0])
		},
	}

	cmd.AddCommand(
		newCheckinsAnswerShowCmd(project),
		newCheckinsAnswerCreateCmd(project),
		newCheckinsAnswerUpdateCmd(project),
	)

	return cmd
}

func newCheckinsAnswerShowCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show answer details",
		Long: `Display details of a check-in answer.

You can pass either an answer ID or a Basecamp URL:
  basecamp checkins answer show 789 --in my-project
  basecamp checkins answer show https://3.basecamp.com/123/buckets/456/question_answers/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheckinsAnswerShow(cmd, *project, args[0])
		},
	}
}

func runCheckinsAnswerShow(cmd *cobra.Command, project, answerIDStr string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Extract ID and project from URL if provided
	answerIDStr, urlProjectID := extractWithProject(answerIDStr)

	// Resolve project - use URL > flag > config, with interactive fallback
	projectID := project
	if projectID == "" && urlProjectID != "" {
		projectID = urlProjectID
	}
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	answerID, err := strconv.ParseInt(answerIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid answer ID")
	}

	answer, err := app.Account().Checkins().GetAnswer(cmd.Context(), answerID)
	if err != nil {
		return convertSDKError(err)
	}

	author := "Unknown"
	if answer.Creator != nil && answer.Creator.Name != "" {
		author = answer.Creator.Name
	}
	date := answer.GroupOn
	if len(date) > 10 {
		date = date[:10]
	}
	summary := fmt.Sprintf("Answer by %s on %s", author, date)

	questionID := ""
	if answer.Parent != nil {
		questionID = strconv.FormatInt(answer.Parent.ID, 10)
	}

	return app.OK(answer,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "question",
				Cmd:         fmt.Sprintf("basecamp checkins question %s --in %s", questionID, resolvedProjectID),
				Description: "View question",
			},
			output.Breadcrumb{
				Action:      "answers",
				Cmd:         fmt.Sprintf("basecamp checkins answers %s --in %s", questionID, resolvedProjectID),
				Description: "View all answers",
			},
		),
	)
}

func newCheckinsAnswerCreateCmd(project *string) *cobra.Command {
	var questionID string
	var content string
	var groupOn string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an answer to a question",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Allow question ID as positional arg
			if questionID == "" && len(args) > 0 {
				questionID = args[0]
			}

			if questionID == "" {
				return output.ErrUsage("--question is required")
			}
			if content == "" {
				return output.ErrUsage("--content is required")
			}

			// Resolve project, with interactive fallback
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			qID, err := strconv.ParseInt(questionID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid question ID")
			}

			req := &basecamp.CreateAnswerRequest{
				Content: fmt.Sprintf("<div>%s</div>", content),
				GroupOn: groupOn,
			}

			answer, err := app.Account().Checkins().CreateAnswer(cmd.Context(), qID, req)
			if err != nil {
				return convertSDKError(err)
			}

			author := "You"
			if answer.Creator != nil && answer.Creator.Name != "" {
				author = answer.Creator.Name
			}

			return app.OK(answer,
				output.WithSummary(fmt.Sprintf("Answer created by %s", author)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "answer",
						Cmd:         fmt.Sprintf("basecamp checkins answer %d --in %s", answer.ID, resolvedProjectID),
						Description: "View answer",
					},
					output.Breadcrumb{
						Action:      "answers",
						Cmd:         fmt.Sprintf("basecamp checkins answers %s --in %s", questionID, resolvedProjectID),
						Description: "View all answers",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&questionID, "question", "", "Question ID to answer (required)")
	cmd.Flags().StringVarP(&content, "content", "c", "", "Answer content (required)")
	cmd.Flags().StringVar(&groupOn, "date", "", "Date to group answer (ISO 8601, e.g., 2024-01-22)")
	_ = cmd.MarkFlagRequired("content")

	return cmd
}

func newCheckinsAnswerUpdateCmd(project *string) *cobra.Command {
	var content string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update an answer",
		Long: `Update an existing check-in answer.

You can pass either an answer ID or a Basecamp URL:
  basecamp checkins answer update 789 --content "updated answer" --in my-project
  basecamp checkins answer update https://3.basecamp.com/123/buckets/456/question_answers/789 --content "updated answer"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			answerIDStr, urlProjectID := extractWithProject(args[0])

			if content == "" {
				return output.ErrUsage("--content is required")
			}

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			answerID, err := strconv.ParseInt(answerIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid answer ID")
			}

			req := &basecamp.UpdateAnswerRequest{
				Content: fmt.Sprintf("<div>%s</div>", content),
			}

			err = app.Account().Checkins().UpdateAnswer(cmd.Context(), answerID, req)
			if err != nil {
				return convertSDKError(err)
			}

			// Fetch the updated answer for display
			answer, err := app.Account().Checkins().GetAnswer(cmd.Context(), answerID)
			if err != nil {
				return convertSDKError(err)
			}

			questionID := ""
			if answer.Parent != nil {
				questionID = strconv.FormatInt(answer.Parent.ID, 10)
			}

			return app.OK(answer,
				output.WithSummary("Answer updated"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "answer",
						Cmd:         fmt.Sprintf("basecamp checkins answer %s --in %s", answerIDStr, resolvedProjectID),
						Description: "View answer",
					},
					output.Breadcrumb{
						Action:      "answers",
						Cmd:         fmt.Sprintf("basecamp checkins answers %s --in %s", questionID, resolvedProjectID),
						Description: "View all answers",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&content, "content", "c", "", "New answer content (required)")
	_ = cmd.MarkFlagRequired("content")

	return cmd
}

// getQuestionnaireID retrieves the questionnaire ID from a project's dock, handling multi-dock projects.
func getQuestionnaireID(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "questionnaire", "", "questionnaire")
}

// parseTimeOfDay parses a time string like "5:00pm" or "17:00" and returns hour and minute.
func parseTimeOfDay(t string) (int, int, error) {
	t = strings.ToLower(strings.TrimSpace(t))

	// Handle 24-hour format
	if strings.Contains(t, ":") && !strings.Contains(t, "am") && !strings.Contains(t, "pm") {
		parts := strings.Split(t, ":")
		if len(parts) != 2 {
			return 0, 0, fmt.Errorf("invalid time format")
		}
		hour, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, err
		}
		minute, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, err
		}
		return hour, minute, nil
	}

	// Handle 12-hour format with am/pm
	isPM := strings.Contains(t, "pm")
	t = strings.TrimSuffix(t, "am")
	t = strings.TrimSuffix(t, "pm")
	t = strings.TrimSpace(t)

	parts := strings.Split(t, ":")
	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}

	minute := 0
	if len(parts) > 1 {
		minute, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, err
		}
	}

	if isPM && hour != 12 {
		hour += 12
	} else if !isPM && hour == 12 {
		hour = 0
	}

	return hour, minute, nil
}
