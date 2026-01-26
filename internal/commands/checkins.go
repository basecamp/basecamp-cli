package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
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

	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
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

	questionnaire, err := app.SDK.Checkins().GetQuestionnaire(cmd.Context(), bucketID, qID)
	if err != nil {
		return convertSDKError(err)
	}

	name := questionnaire.Name
	if name == "" {
		name = "Automatic Check-ins"
	}
	summary := fmt.Sprintf("%s (%d questions)", name, questionnaire.QuestionsCount)

	return app.Output.OK(questionnaire,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "questions",
				Cmd:         fmt.Sprintf("bcq checkins questions --in %s", resolvedProjectID),
				Description: "View questions",
			},
		),
	)
}

func newCheckinsQuestionsCmd(project, questionnaireID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "questions",
		Short: "List check-in questions",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
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

			questions, err := app.SDK.Checkins().ListQuestions(cmd.Context(), bucketID, qID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.Output.OK(questions,
				output.WithSummary(fmt.Sprintf("%d check-in questions", len(questions))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "question",
						Cmd:         fmt.Sprintf("bcq checkins question <id> --in %s", resolvedProjectID),
						Description: "View question details",
					},
					output.Breadcrumb{
						Action:      "answers",
						Cmd:         fmt.Sprintf("bcq checkins answers <question_id> --in %s", resolvedProjectID),
						Description: "View answers",
					},
				),
			)
		},
	}
}

func newCheckinsQuestionCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "question [id]",
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
		Use:   "show <id>",
		Short: "Show question details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheckinsQuestionShow(cmd, *project, args[0])
		},
	}
}

func runCheckinsQuestionShow(cmd *cobra.Command, project, questionIDStr string) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	questionID, err := strconv.ParseInt(questionIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid question ID")
	}

	question, err := app.SDK.Checkins().GetQuestion(cmd.Context(), bucketID, questionID)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("%s (%d answers)", question.Title, question.AnswersCount)

	return app.Output.OK(question,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "answers",
				Cmd:         fmt.Sprintf("bcq checkins answers %s --in %s", questionIDStr, resolvedProjectID),
				Description: "View answers",
			},
			output.Breadcrumb{
				Action:      "questions",
				Cmd:         fmt.Sprintf("bcq checkins questions --in %s", resolvedProjectID),
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

			if title == "" {
				return output.ErrUsage("--title is required")
			}

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
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

			question, err := app.SDK.Checkins().CreateQuestion(cmd.Context(), bucketID, qID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.Output.OK(question,
				output.WithSummary(fmt.Sprintf("Created question #%d: %s", question.ID, question.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "question",
						Cmd:         fmt.Sprintf("bcq checkins question %d --in %s", question.ID, resolvedProjectID),
						Description: "View question",
					},
					output.Breadcrumb{
						Action:      "questions",
						Cmd:         fmt.Sprintf("bcq checkins questions --in %s", resolvedProjectID),
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
		Use:   "update <id>",
		Short: "Update a check-in question",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			questionIDStr := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
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

			question, err := app.SDK.Checkins().UpdateQuestion(cmd.Context(), bucketID, questionID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.Output.OK(question,
				output.WithSummary(fmt.Sprintf("Updated question #%s: %s", questionIDStr, question.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "question",
						Cmd:         fmt.Sprintf("bcq checkins question %s --in %s", questionIDStr, resolvedProjectID),
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
	return &cobra.Command{
		Use:   "answers <question_id>",
		Short: "List answers for a question",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			questionIDStr := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			questionID, err := strconv.ParseInt(questionIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid question ID")
			}

			answers, err := app.SDK.Checkins().ListAnswers(cmd.Context(), bucketID, questionID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.Output.OK(answers,
				output.WithSummary(fmt.Sprintf("%d answers", len(answers))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "answer",
						Cmd:         fmt.Sprintf("bcq checkins answer <id> --in %s", resolvedProjectID),
						Description: "View answer details",
					},
					output.Breadcrumb{
						Action:      "question",
						Cmd:         fmt.Sprintf("bcq checkins question %s --in %s", questionIDStr, resolvedProjectID),
						Description: "View question",
					},
				),
			)
		},
	}
}

func newCheckinsAnswerCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "answer [id]",
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
		Use:   "show <id>",
		Short: "Show answer details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheckinsAnswerShow(cmd, *project, args[0])
		},
	}
}

func runCheckinsAnswerShow(cmd *cobra.Command, project, answerIDStr string) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	answerID, err := strconv.ParseInt(answerIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid answer ID")
	}

	answer, err := app.SDK.Checkins().GetAnswer(cmd.Context(), bucketID, answerID)
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

	return app.Output.OK(answer,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "question",
				Cmd:         fmt.Sprintf("bcq checkins question %s --in %s", questionID, resolvedProjectID),
				Description: "View question",
			},
			output.Breadcrumb{
				Action:      "answers",
				Cmd:         fmt.Sprintf("bcq checkins answers %s --in %s", questionID, resolvedProjectID),
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

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			qID, err := strconv.ParseInt(questionID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid question ID")
			}

			req := &basecamp.CreateAnswerRequest{
				Content: fmt.Sprintf("<div>%s</div>", content),
				GroupOn: groupOn,
			}

			answer, err := app.SDK.Checkins().CreateAnswer(cmd.Context(), bucketID, qID, req)
			if err != nil {
				return convertSDKError(err)
			}

			author := "You"
			if answer.Creator != nil && answer.Creator.Name != "" {
				author = answer.Creator.Name
			}

			return app.Output.OK(answer,
				output.WithSummary(fmt.Sprintf("Answer created by %s", author)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "answer",
						Cmd:         fmt.Sprintf("bcq checkins answer %d --in %s", answer.ID, resolvedProjectID),
						Description: "View answer",
					},
					output.Breadcrumb{
						Action:      "answers",
						Cmd:         fmt.Sprintf("bcq checkins answers %s --in %s", questionID, resolvedProjectID),
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
		Use:   "update <id>",
		Short: "Update an answer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			answerIDStr := args[0]

			if content == "" {
				return output.ErrUsage("--content is required")
			}

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			answerID, err := strconv.ParseInt(answerIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid answer ID")
			}

			req := &basecamp.UpdateAnswerRequest{
				Content: fmt.Sprintf("<div>%s</div>", content),
			}

			err = app.SDK.Checkins().UpdateAnswer(cmd.Context(), bucketID, answerID, req)
			if err != nil {
				return convertSDKError(err)
			}

			// Fetch the updated answer for display
			answer, err := app.SDK.Checkins().GetAnswer(cmd.Context(), bucketID, answerID)
			if err != nil {
				return convertSDKError(err)
			}

			questionID := ""
			if answer.Parent != nil {
				questionID = strconv.FormatInt(answer.Parent.ID, 10)
			}

			return app.Output.OK(answer,
				output.WithSummary("Answer updated"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "answer",
						Cmd:         fmt.Sprintf("bcq checkins answer %s --in %s", answerIDStr, resolvedProjectID),
						Description: "View answer",
					},
					output.Breadcrumb{
						Action:      "answers",
						Cmd:         fmt.Sprintf("bcq checkins answers %s --in %s", questionID, resolvedProjectID),
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
