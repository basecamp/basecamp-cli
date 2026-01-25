package commands

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

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
	if err := app.API.RequireAccount(); err != nil {
		return err
	}

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

	// Get questionnaire ID
	resolvedQuestionnaireID := questionnaireID
	if resolvedQuestionnaireID == "" {
		resolvedQuestionnaireID, err = getQuestionnaireID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	path := fmt.Sprintf("/buckets/%s/questionnaires/%s.json", resolvedProjectID, resolvedQuestionnaireID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var data struct {
		Name           string `json:"name"`
		QuestionsCount int    `json:"questions_count"`
	}
	json.Unmarshal(resp.Data, &data)

	name := data.Name
	if name == "" {
		name = "Automatic Check-ins"
	}
	summary := fmt.Sprintf("%s (%d questions)", name, data.QuestionsCount)

	return app.Output.OK(json.RawMessage(resp.Data),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
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

			// Get questionnaire ID
			resolvedQuestionnaireID := *questionnaireID
			if resolvedQuestionnaireID == "" {
				resolvedQuestionnaireID, err = getQuestionnaireID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			path := fmt.Sprintf("/buckets/%s/questionnaires/%s/questions.json", resolvedProjectID, resolvedQuestionnaireID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var questions []any
			if err := resp.UnmarshalData(&questions); err != nil {
				return fmt.Errorf("failed to parse questions: %w", err)
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

func runCheckinsQuestionShow(cmd *cobra.Command, project, questionID string) error {
	app := appctx.FromContext(cmd.Context())
	if err := app.API.RequireAccount(); err != nil {
		return err
	}

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

	path := fmt.Sprintf("/buckets/%s/questions/%s.json", resolvedProjectID, questionID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var data struct {
		Title        string `json:"title"`
		AnswersCount int    `json:"answers_count"`
	}
	json.Unmarshal(resp.Data, &data)

	summary := fmt.Sprintf("%s (%d answers)", data.Title, data.AnswersCount)

	return app.Output.OK(json.RawMessage(resp.Data),
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "answers",
				Cmd:         fmt.Sprintf("bcq checkins answers %s --in %s", questionID, resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

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

			// Get questionnaire ID
			resolvedQuestionnaireID := questionnaireID
			if resolvedQuestionnaireID == "" {
				resolvedQuestionnaireID, err = getQuestionnaireID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			// Default values
			if frequency == "" {
				frequency = "every_day"
			}
			if timeOfDay == "" {
				timeOfDay = "5:00pm"
			}
			if days == "" {
				days = "1,2,3,4,5"
			}

			// Parse days into array
			dayParts := strings.Split(days, ",")
			daysArray := make([]string, 0, len(dayParts))
			for _, d := range dayParts {
				d = strings.TrimSpace(d)
				if d != "" {
					daysArray = append(daysArray, d)
				}
			}

			body := map[string]any{
				"question": map[string]any{
					"title": title,
					"schedule": map[string]any{
						"frequency":   frequency,
						"time_of_day": timeOfDay,
						"days":        daysArray,
					},
				},
			}

			path := fmt.Sprintf("/buckets/%s/questionnaires/%s/questions.json", resolvedProjectID, resolvedQuestionnaireID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var question struct {
				ID    int64  `json:"id"`
				Title string `json:"title"`
			}
			json.Unmarshal(resp.Data, &question)

			return app.Output.OK(json.RawMessage(resp.Data),
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
	cmd.MarkFlagRequired("title")

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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			questionID := args[0]

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

			// Build payload
			question := make(map[string]any)
			if title != "" {
				question["title"] = title
			}

			if frequency != "" || timeOfDay != "" || days != "" {
				schedule := make(map[string]any)
				if frequency != "" {
					schedule["frequency"] = frequency
				}
				if timeOfDay != "" {
					schedule["time_of_day"] = timeOfDay
				}
				if days != "" {
					dayParts := strings.Split(days, ",")
					daysArray := make([]string, 0, len(dayParts))
					for _, d := range dayParts {
						d = strings.TrimSpace(d)
						if d != "" {
							daysArray = append(daysArray, d)
						}
					}
					schedule["days"] = daysArray
				}
				question["schedule"] = schedule
			}

			if len(question) == 0 {
				return output.ErrUsage("at least one of --title, --frequency, --time, or --days is required")
			}

			body := map[string]any{
				"question": question,
			}

			path := fmt.Sprintf("/buckets/%s/questions/%s.json", resolvedProjectID, questionID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var data struct {
				Title string `json:"title"`
			}
			json.Unmarshal(resp.Data, &data)

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Updated question #%s: %s", questionID, data.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "question",
						Cmd:         fmt.Sprintf("bcq checkins question %s --in %s", questionID, resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			questionID := args[0]

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

			path := fmt.Sprintf("/buckets/%s/questions/%s/answers.json", resolvedProjectID, questionID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var answers []any
			if err := resp.UnmarshalData(&answers); err != nil {
				return fmt.Errorf("failed to parse answers: %w", err)
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
						Cmd:         fmt.Sprintf("bcq checkins question %s --in %s", questionID, resolvedProjectID),
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

func runCheckinsAnswerShow(cmd *cobra.Command, project, answerID string) error {
	app := appctx.FromContext(cmd.Context())
	if err := app.API.RequireAccount(); err != nil {
		return err
	}

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

	path := fmt.Sprintf("/buckets/%s/question_answers/%s.json", resolvedProjectID, answerID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var data struct {
		Creator struct {
			Name string `json:"name"`
		} `json:"creator"`
		GroupOn string `json:"group_on"`
		Parent  struct {
			ID int64 `json:"id"`
		} `json:"parent"`
	}
	json.Unmarshal(resp.Data, &data)

	author := data.Creator.Name
	if author == "" {
		author = "Unknown"
	}
	date := data.GroupOn
	if len(date) > 10 {
		date = date[:10]
	}
	summary := fmt.Sprintf("Answer by %s on %s", author, date)

	questionID := strconv.FormatInt(data.Parent.ID, 10)

	return app.Output.OK(json.RawMessage(resp.Data),
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
			if err := app.API.RequireAccount(); err != nil {
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

			answer := map[string]any{
				"content": fmt.Sprintf("<div>%s</div>", content),
			}
			if groupOn != "" {
				answer["group_on"] = groupOn
			}

			body := map[string]any{
				"question_answer": answer,
			}

			path := fmt.Sprintf("/buckets/%s/questions/%s/answers.json", resolvedProjectID, questionID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var data struct {
				ID      int64 `json:"id"`
				Creator struct {
					Name string `json:"name"`
				} `json:"creator"`
			}
			json.Unmarshal(resp.Data, &data)

			author := data.Creator.Name
			if author == "" {
				author = "You"
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Answer created by %s", author)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "answer",
						Cmd:         fmt.Sprintf("bcq checkins answer %d --in %s", data.ID, resolvedProjectID),
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
	cmd.MarkFlagRequired("content")

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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			answerID := args[0]

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

			body := map[string]any{
				"question_answer": map[string]any{
					"content": fmt.Sprintf("<div>%s</div>", content),
				},
			}

			path := fmt.Sprintf("/buckets/%s/question_answers/%s.json", resolvedProjectID, answerID)
			_, err = app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			// Fetch the updated answer
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var data struct {
				Parent struct {
					ID int64 `json:"id"`
				} `json:"parent"`
			}
			json.Unmarshal(resp.Data, &data)

			questionID := strconv.FormatInt(data.Parent.ID, 10)

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary("Answer updated"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "answer",
						Cmd:         fmt.Sprintf("bcq checkins answer %s --in %s", answerID, resolvedProjectID),
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
	cmd.MarkFlagRequired("content")

	return cmd
}

// getQuestionnaireID retrieves the questionnaire ID from a project's dock, handling multi-dock projects.
func getQuestionnaireID(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "questionnaire", "", "questionnaire")
}
