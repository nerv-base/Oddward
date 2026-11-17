// SPDX-License-Identifier: GPL-3.0-or-later
// Pure WPF presentation composition for the native LoginHost window.
// This file owns layout and styling only; event handlers and auth state remain in the caller.
//
// Visual language: a dark bezel around a light-grey card, a near-black header
// with vector close/back glyphs, pill-shaped fields and actions. The header
// carries Oddward's own wordmark and an explicit "非官方" tag — never a
// publisher logo.
using System;
using System.ComponentModel;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
using System.Windows.Media;
using System.Windows.Media.Effects;
using System.Windows.Media.Imaging;
using System.Windows.Shapes;
using Path = System.Windows.Shapes.Path;

namespace Nte.LoginHost.Presentation;

internal sealed record LoginParts(
    TextBlock Title,
    TextBlock Subtitle,
    StackPanel Form,
    StackPanel Footer,
    TextBlock Status,
    Button Close,
    Button Primary,
    Button OtherAccount,
    Button OfficialLauncher,
    CheckBox Remember,
    string HelpLabel,
    string BackLabel);

internal sealed class LoginSurface
{
    internal const double Width = 420;
    internal const double Height = 520;
    internal const double CardRadius = 18;
    internal const double BezelWidth = 6;
    internal const double FrameRadius = 12;
    internal const double HeaderHeight = 64;
    internal const double FieldWidth = 344;
    internal const double FieldHeight = 52;
    internal const double PrimaryHeight = 52;
    internal const double SecondaryHeight = 44;
    internal const double FooterHeight = 144;
    internal const double FooterInset = 18;
    internal const double IconSize = 44;

    private readonly LoginParts parts;
    private readonly ScrollViewer statusScroll;
    private readonly Grid headerActions;
    private readonly StackPanel footerHost;
    private readonly Grid footerLayout;
    private bool modalEnabled = true;

    internal Border Card { get; }
    // Keep a Grid as Card.Child: Notice.cs adds its confirmation overlay here.
    internal Grid ContentRoot { get; }
    internal Grid Frame { get; }
    internal Grid HeaderBar { get; }
    internal Border StatusChip { get; }
    internal ScrollViewer FormScroll { get; }
    internal Button Help { get; }
    internal Button Back { get; }

    private LoginSurface(LoginParts p)
    {
        parts = p;
        Help = new Button { Content = HelpGlyph(), ToolTip = p.HelpLabel };
        Back = new Button { Content = BackGlyph(), ToolTip = p.BackLabel };

        ContentRoot = new Grid { Width = Width, Height = Height };
        ContentRoot.Children.Add(new Border
        {
            CornerRadius = new CornerRadius(CardRadius),
            Background = new LinearGradientBrush(Color.FromRgb(0x4B, 0x4C, 0x51), Color.FromRgb(0x2A, 0x2B, 0x2F), 90),
            BorderBrush = new SolidColorBrush(Color.FromArgb(0x66, 0x7A, 0x7B, 0x80)),
            BorderThickness = new Thickness(1),
            Effect = new DropShadowEffect { BlurRadius = 28, ShadowDepth = 6, Direction = 270, Opacity = .42, Color = Colors.Black },
            IsHitTestVisible = false
        });

        double frameWidth = Width - 2 * BezelWidth, frameHeight = Height - 2 * BezelWidth;
        Frame = new Grid
        {
            Margin = new Thickness(BezelWidth),
            Background = LoginVisuals.Brush(LoginVisuals.Body),
            Clip = new RectangleGeometry(new Rect(0, 0, frameWidth, frameHeight), FrameRadius, FrameRadius)
        };
        Frame.RowDefinitions.Add(new RowDefinition { Height = new GridLength(HeaderHeight) });
        Frame.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        Frame.RowDefinitions.Add(new RowDefinition { Height = new GridLength(FooterHeight + FooterInset) });
        ContentRoot.Children.Add(Frame);

        Rectangle texture = new() { Fill = Crosshatch(), OpacityMask = TextureFade(), IsHitTestVisible = false };
        Grid.SetRow(texture, 1);
        Grid.SetRowSpan(texture, 2);
        Frame.Children.Add(texture);

        HeaderBar = new Grid { Background = LoginVisuals.Brush(LoginVisuals.Header) };
        HeaderBar.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(104) });
        HeaderBar.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        HeaderBar.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(104) });
        Frame.Children.Add(HeaderBar);

        StackPanel leading = new()
        {
            Orientation = Orientation.Horizontal,
            VerticalAlignment = VerticalAlignment.Center,
            Margin = new Thickness(10, 0, 0, 0)
        };
        Attach(leading, Back);
        Attach(leading, Help);
        HeaderBar.Children.Add(leading);

        FrameworkElement brand = Brand();
        Grid.SetColumn(brand, 1);
        HeaderBar.Children.Add(brand);

        headerActions = new Grid
        {
            HorizontalAlignment = HorizontalAlignment.Right,
            VerticalAlignment = VerticalAlignment.Center,
            Margin = new Thickness(0, 0, 10, 0)
        };
        Attach(headerActions, p.Close, HorizontalAlignment.Right, new Thickness(0));
        Grid.SetColumn(headerActions, 2);
        HeaderBar.Children.Add(headerActions);

        Rectangle seam = new()
        {
            Height = 2,
            VerticalAlignment = VerticalAlignment.Top,
            Fill = new SolidColorBrush(Color.FromRgb(0xB4, 0xB5, 0xB8)),
            IsHitTestVisible = false
        };
        Grid.SetRow(seam, 1);
        Frame.Children.Add(seam);

        Grid body = new();
        body.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        body.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        body.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        Grid.SetRow(body, 1);
        Frame.Children.Add(body);

        StackPanel heading = new() { Margin = new Thickness(32, 22, 32, 0), HorizontalAlignment = HorizontalAlignment.Center };
        ConfigureText(p.Title, 16, FontWeights.Bold, LoginVisuals.Brush(LoginVisuals.Text));
        ConfigureText(p.Subtitle, 12, FontWeights.Normal, LoginVisuals.Brush(LoginVisuals.MutedText));
        p.Title.TextAlignment = TextAlignment.Center;
        p.Subtitle.TextAlignment = TextAlignment.Center;
        p.Subtitle.Margin = new Thickness(0, 4, 0, 0);
        Attach(heading, p.Title);
        Attach(heading, p.Subtitle);
        body.Children.Add(heading);

        StatusChip = new Border
        {
            CornerRadius = new CornerRadius(14),
            Background = LoginVisuals.Brush(LoginVisuals.Status),
            Padding = new Thickness(14, 9, 14, 9)
        };
        ConfigureText(p.Status, 12.5, FontWeights.Normal, Brushes.White);
        p.Status.Background = Brushes.Transparent;
        p.Status.Padding = new Thickness(0);
        p.Status.MinHeight = 0;
        SetAccessible(p.Status, "登录状态");
        Detach(p.Status);
        StatusChip.Child = p.Status;
        statusScroll = new ScrollViewer
        {
            Margin = new Thickness(32, 12, 32, 0),
            MaxHeight = 76,
            VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
            HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled,
            Style = LoginVisuals.Style(LoginVisuals.Scroll),
            Content = StatusChip,
            Visibility = Visibility.Collapsed
        };
        Grid.SetRow(statusScroll, 1);
        body.Children.Add(statusScroll);
        DependencyPropertyDescriptor.FromProperty(TextBlock.TextProperty, typeof(TextBlock))
            ?.AddValueChanged(p.Status, (_, _) => RefreshStatus());

        FormScroll = new ScrollViewer
        {
            Margin = new Thickness(24, 16, 24, 8),
            VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
            HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled,
            Style = LoginVisuals.Style(LoginVisuals.Scroll)
        };
        p.Form.Orientation = Orientation.Vertical;
        p.Form.Width = FieldWidth;
        p.Form.HorizontalAlignment = HorizontalAlignment.Center;
        p.Form.VerticalAlignment = VerticalAlignment.Top;
        p.Form.Margin = new Thickness(0);
        p.Form.SetValue(AutomationProperties.NameProperty, "登录表单");
        Attach(FormScroll, p.Form);
        Grid.SetRow(FormScroll, 2);
        body.Children.Add(FormScroll);

        footerHost = p.Footer;
        footerHost.Orientation = Orientation.Vertical;
        footerHost.HorizontalAlignment = HorizontalAlignment.Center;
        footerHost.VerticalAlignment = VerticalAlignment.Top;
        footerHost.Width = FieldWidth;
        footerHost.Height = FooterHeight;
        footerHost.SetValue(AutomationProperties.NameProperty, "登录操作");
        // Primary pill, a full-width secondary pill (split in two when both
        // secondary actions are visible) and the save-account line at the
        // bottom, like an agreement line: 52 + 12 + 44 + 10 + 26 = 144 DIP.
        footerLayout = new Grid { Width = FieldWidth, Height = FooterHeight };
        foreach (double row in new[] { PrimaryHeight, 12, SecondaryHeight, 10, 26 })
            footerLayout.RowDefinitions.Add(new RowDefinition { Height = new GridLength(row) });
        footerLayout.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        footerLayout.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(10) });
        footerLayout.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        footerHost.Children.Clear();
        footerHost.Children.Add(footerLayout);
        Grid footerGrid = new();
        Grid.SetRow(footerGrid, 2);
        Frame.Children.Add(footerGrid);
        Attach(footerGrid, footerHost, HorizontalAlignment.Center, new Thickness(0, 0, 0, FooterInset));

        Attach(footerLayout, p.Primary, HorizontalAlignment.Stretch, new Thickness(0));
        Grid.SetColumnSpan(p.Primary, 3);
        Attach(footerLayout, p.OtherAccount, HorizontalAlignment.Stretch, new Thickness(0));
        Grid.SetRow(p.OtherAccount, 2);
        Attach(footerLayout, p.OfficialLauncher, HorizontalAlignment.Stretch, new Thickness(0));
        Grid.SetRow(p.OfficialLauncher, 2);
        PlaceRemember();

        string closeName = ContentLabel(p.Close, "关闭");
        p.Close.Content = CloseGlyph();
        p.Close.ToolTip ??= closeName;
        ConfigureButton(p.Close, closeName, IconSize, IconSize, LoginVisuals.GhostButton);
        ConfigureButton(Help, p.HelpLabel, IconSize, IconSize, LoginVisuals.GhostButton);
        ConfigureButton(Back, p.BackLabel, IconSize, IconSize, LoginVisuals.GhostButton);
        ConfigureButton(p.Primary, ContentLabel(p.Primary, "登录"), FieldWidth, PrimaryHeight, LoginVisuals.PrimaryButton);
        ConfigureButton(p.OtherAccount, ContentLabel(p.OtherAccount, "其他账号"), double.NaN, SecondaryHeight, LoginVisuals.SecondaryButton);
        ConfigureButton(p.OfficialLauncher, ContentLabel(p.OfficialLauncher, "官方启动器"), double.NaN, SecondaryHeight, LoginVisuals.SecondaryButton);
        ConfigureCheckBox(p.Remember);
        p.Close.IsCancel = true;
        p.Primary.IsDefault = true;
        DependencyPropertyDescriptor visibility = DependencyPropertyDescriptor.FromProperty(UIElement.VisibilityProperty, typeof(UIElement));
        visibility?.AddValueChanged(p.OtherAccount, (_, _) => ArrangeSecondary());
        visibility?.AddValueChanged(p.OfficialLauncher, (_, _) => ArrangeSecondary());
        ArrangeSecondary();

        Card = new Border
        {
            Width = Width,
            Height = Height,
            Background = Brushes.Transparent,
            BorderThickness = new Thickness(0),
            CornerRadius = new CornerRadius(CardRadius),
            Child = ContentRoot
        };
        Card.Resources = LoginVisuals.Create();
        SetAccessible(Card, "社区/非官方登录窗口");
        RefreshStatus();
        SetPresentation(false, false, false, false);
    }

    internal static LoginSurface Compose(LoginParts p)
    {
        ArgumentNullException.ThrowIfNull(p);
        return new LoginSurface(p);
    }

    internal void SetModalEnabled(bool enabled)
    {
        modalEnabled = enabled;
        FormScroll.IsEnabled = enabled;
        footerHost.IsEnabled = enabled;
        headerActions.IsEnabled = enabled;
    }

    internal void SetPresentation(bool notice, bool savedAccounts, bool globalContent, bool canBack)
    {
        Back.Visibility = canBack ? Visibility.Visible : Visibility.Collapsed;
        Help.Visibility = notice ? Visibility.Collapsed : Visibility.Visible;
        parts.Remember.Visibility = notice || savedAccounts || globalContent
            ? Visibility.Collapsed
            : Visibility.Visible;
        // The mandatory notice is a readable, actionable state: keep both the
        // document scroll viewer and its acknowledgement/official-launcher
        // controls interactive. The caller still owns all business handlers.
        SetModalEnabled(modalEnabled);
        // ShowForm may reattach the shared save-account control to the upstream
        // form. Restore its presentation parent before applying state so every
        // subsequent mode/notice transition is safe for the same control instance.
        PlaceRemember();
        ArrangeSecondary();
        RefreshStatus();
    }

    internal static Grid Input(TextBox input, string placeholder, Button? suffix = null, string? prefix = null)
    {
        ArgumentNullException.ThrowIfNull(input);
        Grid field = CreateInputShell(input, placeholder, prefix, suffix);
        TextBlock hint = (TextBlock)field.Children[^1];
        input.TextChanged += (_, _) => hint.Visibility = input.Text.Length == 0 ? Visibility.Visible : Visibility.Hidden;
        hint.Visibility = input.Text.Length == 0 ? Visibility.Visible : Visibility.Hidden;
        if (suffix is not null)
        {
            // Preserve the upstream localized SMS content/cooldown; it reads as
            // bold text inside the pill, the way a resend countdown usually does.
            ConfigureButton(suffix, ContentLabel(suffix, "发送验证码"), 120, 36, LoginVisuals.LinkButton);
            Attach(field, suffix, HorizontalAlignment.Right, new Thickness(0, 8, 10, 8));
        }
        SetAccessible(input, string.IsNullOrWhiteSpace(placeholder) ? "输入" : placeholder);
        return field;
    }

    internal static Grid Input(PasswordBox input, string placeholder)
    {
        ArgumentNullException.ThrowIfNull(input);
        Grid field = CreateInputShell(input, placeholder, null, null);
        TextBlock hint = (TextBlock)field.Children[^1];
        input.PasswordChanged += (_, _) => hint.Visibility = input.Password.Length == 0 ? Visibility.Visible : Visibility.Hidden;
        SetAccessible(input, string.IsNullOrWhiteSpace(placeholder) ? "密码" : placeholder);
        return field;
    }

    // Placeholders inside the pills replace visible labels; the label stays
    // as the accessible name of the group.
    internal static StackPanel Group(string accessibleLabel, UIElement field)
    {
        ArgumentNullException.ThrowIfNull(field);
        StackPanel group = new()
        {
            Orientation = Orientation.Vertical,
            Width = FieldWidth,
            Margin = new Thickness(0, 0, 0, 14)
        };
        Attach(group, field);
        SetAccessible(group, accessibleLabel);
        return group;
    }

    private static Grid CreateInputShell(Control input, string placeholder, string? prefix, Button? suffix)
    {
        Grid field = new() { Width = FieldWidth, Height = FieldHeight };
        Border shell = new()
        {
            Background = Brushes.White,
            BorderBrush = LoginVisuals.Brush(LoginVisuals.InputBorder),
            BorderThickness = new Thickness(1.5),
            CornerRadius = new CornerRadius(FieldHeight / 2)
        };
        field.Children.Add(shell);
        field.IsKeyboardFocusWithinChanged += (_, args) =>
            shell.BorderBrush = (bool)args.NewValue
                ? new SolidColorBrush(Color.FromRgb(0x55, 0x57, 0x5C))
                : LoginVisuals.Brush(LoginVisuals.InputBorder);

        double left = 22;
        if (!string.IsNullOrEmpty(prefix))
        {
            field.Children.Add(new TextBlock
            {
                Text = prefix,
                FontFamily = LoginVisuals.Font,
                FontSize = 15,
                FontWeight = FontWeights.Bold,
                Foreground = LoginVisuals.Brush(LoginVisuals.Text),
                VerticalAlignment = VerticalAlignment.Center,
                Margin = new Thickness(22, 0, 0, 0),
                IsHitTestVisible = false
            });
            field.Children.Add(new Rectangle
            {
                Width = 2,
                Height = 24,
                RadiusX = 1,
                RadiusY = 1,
                Fill = LoginVisuals.Brush(LoginVisuals.InputBorder),
                HorizontalAlignment = HorizontalAlignment.Left,
                Margin = new Thickness(66, 0, 0, 0),
                IsHitTestVisible = false
            });
            left = 82;
        }

        double right = suffix is null ? 22 : 140;
        input.Height = FieldHeight - 4;
        input.Width = double.NaN;
        input.HorizontalAlignment = HorizontalAlignment.Stretch;
        input.Margin = new Thickness(left, 2, right, 2);
        input.Padding = new Thickness(0);
        input.VerticalContentAlignment = VerticalAlignment.Center;
        input.HorizontalContentAlignment = HorizontalAlignment.Left;
        input.FontFamily = LoginVisuals.Font;
        input.FontSize = 15;
        input.FontWeight = FontWeights.SemiBold;
        input.Foreground = LoginVisuals.Brush(LoginVisuals.Text);
        input.Background = Brushes.Transparent;
        input.BorderThickness = new Thickness(0);
        input.ToolTip = placeholder;
        Attach(field, input, HorizontalAlignment.Stretch, input.Margin);

        // The hint is always the last child; Input() binds its visibility.
        field.Children.Add(new TextBlock
        {
            Text = placeholder,
            FontFamily = LoginVisuals.Font,
            FontSize = 15,
            Foreground = LoginVisuals.Brush(LoginVisuals.MutedText),
            VerticalAlignment = VerticalAlignment.Center,
            Margin = new Thickness(left + 2, 0, right, 0),
            TextTrimming = TextTrimming.CharacterEllipsis,
            IsHitTestVisible = false
        });
        return field;
    }

    private void PlaceRemember()
    {
        Attach(footerLayout, parts.Remember, HorizontalAlignment.Center, new Thickness(0));
        Grid.SetRow(parts.Remember, 4);
        Grid.SetColumn(parts.Remember, 0);
        Grid.SetColumnSpan(parts.Remember, 3);
    }

    private void ArrangeSecondary()
    {
        bool both = parts.OtherAccount.Visibility == Visibility.Visible &&
                    parts.OfficialLauncher.Visibility == Visibility.Visible;
        Grid.SetColumn(parts.OtherAccount, 0);
        Grid.SetColumnSpan(parts.OtherAccount, both ? 1 : 3);
        Grid.SetColumn(parts.OfficialLauncher, both ? 2 : 0);
        Grid.SetColumnSpan(parts.OfficialLauncher, both ? 1 : 3);
    }

    private static FrameworkElement Brand()
    {
        StackPanel brand = new()
        {
            Orientation = Orientation.Horizontal,
            HorizontalAlignment = HorizontalAlignment.Center,
            VerticalAlignment = VerticalAlignment.Center,
            IsHitTestVisible = false
        };
        Image mark = new()
        {
            Source = new BitmapImage(new Uri("pack://application:,,,/Branding/symbol.png", UriKind.Absolute)),
            Width = 24,
            Height = 24,
            Stretch = Stretch.Uniform,
            Margin = new Thickness(0, 0, 9, 0),
            VerticalAlignment = VerticalAlignment.Center
        };
        RenderOptions.SetBitmapScalingMode(mark, BitmapScalingMode.HighQuality);
        brand.Children.Add(mark);
        brand.Children.Add(new TextBlock
        {
            Text = "ODDWARD",
            FontFamily = LoginVisuals.Wordmark,
            FontSize = 21,
            Foreground = Brushes.White,
            VerticalAlignment = VerticalAlignment.Center
        });
        brand.Children.Add(new Border
        {
            Margin = new Thickness(9, 1, 0, 0),
            Padding = new Thickness(5, 1, 5, 2),
            CornerRadius = new CornerRadius(4),
            BorderThickness = new Thickness(1),
            BorderBrush = new SolidColorBrush(Color.FromArgb(0x80, 0xFF, 0xFF, 0xFF)),
            VerticalAlignment = VerticalAlignment.Center,
            Child = new TextBlock
            {
                Text = "非官方",
                FontFamily = LoginVisuals.Font,
                FontSize = 10,
                Foreground = new SolidColorBrush(Color.FromArgb(0xCC, 0xFF, 0xFF, 0xFF))
            }
        });
        AutomationProperties.SetName(brand, "Oddward 社区启动器 · 非官方");
        return brand;
    }

    // A faceted cross: four tapered blades meeting in a pinched centre.
    private static UIElement CloseGlyph() => new Path
    {
        Data = Geometry.Parse("M1,1 L12,8.6 L23,1 L15.4,12 L23,23 L12,15.4 L1,23 L8.6,12 Z"),
        Fill = Brushes.White,
        Width = 22,
        Height = 22,
        Stretch = Stretch.Uniform
    };

    private static UIElement BackGlyph() => new Path
    {
        Data = Geometry.Parse("M15,3 L6,12 L15,21"),
        Stroke = Brushes.White,
        StrokeThickness = 3.6,
        StrokeStartLineCap = PenLineCap.Round,
        StrokeEndLineCap = PenLineCap.Round,
        StrokeLineJoin = PenLineJoin.Round,
        Width = 22,
        Height = 22,
        Stretch = Stretch.Uniform
    };

    private static UIElement HelpGlyph()
    {
        Grid glyph = new() { Width = 24, Height = 24 };
        glyph.Children.Add(new Ellipse { Stroke = Brushes.White, StrokeThickness = 2 });
        glyph.Children.Add(new TextBlock
        {
            Text = "?",
            FontFamily = LoginVisuals.Font,
            FontSize = 14,
            FontWeight = FontWeights.Bold,
            Foreground = Brushes.White,
            HorizontalAlignment = HorizontalAlignment.Center,
            VerticalAlignment = VerticalAlignment.Center,
            Margin = new Thickness(0, 0, 0, 1)
        });
        return glyph;
    }

    private static Brush Crosshatch()
    {
        GeometryDrawing lines = new()
        {
            Geometry = Geometry.Parse("M0,0 L8,8 M8,0 L0,8"),
            Pen = new Pen(new SolidColorBrush(Color.FromArgb(0x10, 0, 0, 0)), .8)
        };
        DrawingBrush brush = new(lines)
        {
            TileMode = TileMode.Tile,
            Viewport = new Rect(0, 0, 8, 8),
            ViewportUnits = BrushMappingMode.Absolute,
            Viewbox = new Rect(0, 0, 8, 8),
            ViewboxUnits = BrushMappingMode.Absolute
        };
        brush.Freeze();
        return brush;
    }

    private static Brush TextureFade()
    {
        LinearGradientBrush fade = new() { StartPoint = new Point(0, 0), EndPoint = new Point(0, 1) };
        fade.GradientStops.Add(new GradientStop(Color.FromArgb(0x20, 0, 0, 0), 0));
        fade.GradientStops.Add(new GradientStop(Color.FromArgb(0xFF, 0, 0, 0), 1));
        fade.Freeze();
        return fade;
    }

    private static void ConfigureText(TextBlock text, double size, FontWeight weight, Brush foreground)
    {
        text.FontFamily = LoginVisuals.Font;
        text.FontSize = size;
        text.FontWeight = weight;
        text.Foreground = foreground;
        text.TextWrapping = TextWrapping.Wrap;
    }

    private static void ConfigureButton(Button button, string accessibleName, double width, double height, string style)
    {
        button.Width = width;
        button.Height = height;
        button.MinWidth = double.IsNaN(width) ? 0 : width;
        button.MinHeight = height;
        button.Style = LoginVisuals.Style(style);
        button.ClearValue(Control.PaddingProperty);
        button.ClearValue(Control.FontSizeProperty);
        SetAccessible(button, accessibleName);
    }

    private static void ConfigureCheckBox(CheckBox checkBox)
    {
        checkBox.Height = 26;
        checkBox.Style = LoginVisuals.Style(LoginVisuals.CheckBox);
        checkBox.ClearValue(Control.FontSizeProperty);
        checkBox.Content ??= "记住账号";
        SetAccessible(checkBox, ContentLabel(checkBox, "记住账号"));
    }

    private static string ContentLabel(ContentControl control, string fallback)
    {
        string existing = AutomationProperties.GetName(control);
        if (!string.IsNullOrWhiteSpace(existing)) return existing;
        return control.Content switch
        {
            TextBlock text when !string.IsNullOrWhiteSpace(text.Text) => text.Text,
            string text when !string.IsNullOrWhiteSpace(text) => text,
            _ => fallback
        };
    }

    private void RefreshStatus()
    {
        statusScroll.Visibility = string.IsNullOrWhiteSpace(parts.Status.Text)
            ? Visibility.Collapsed
            : Visibility.Visible;
    }

    private static void SetAccessible(DependencyObject element, string name) =>
        AutomationProperties.SetName(element, name);

    private static void Attach(Panel panel, UIElement element) => Attach(panel, element, HorizontalAlignment.Stretch, new Thickness(0));

    private static void Attach(Panel panel, UIElement element, HorizontalAlignment alignment, Thickness margin)
    {
        if (element is FrameworkElement frameworkElement)
        {
            frameworkElement.HorizontalAlignment = alignment;
            frameworkElement.Margin = margin;
        }
        if (ReferenceEquals(ParentOf(element), panel))
            return;
        Detach(element);
        panel.Children.Add(element);
    }

    private static void Attach(ScrollViewer viewer, UIElement element)
    {
        if (ReferenceEquals(ParentOf(element), viewer))
            return;
        Detach(element);
        viewer.Content = element;
    }

    private static DependencyObject? ParentOf(UIElement element) =>
        element is FrameworkElement frameworkElement ? frameworkElement.Parent : null;

    private static void Detach(UIElement element)
    {
        switch (ParentOf(element))
        {
            case Panel panel:
                panel.Children.Remove(element);
                break;
            case ContentControl content when ReferenceEquals(content.Content, element):
                content.Content = null;
                break;
            case Decorator decorator when ReferenceEquals(decorator.Child, element):
                decorator.Child = null;
                break;
        }
    }
}
