// SPDX-License-Identifier: GPL-3.0-or-later
// Pure system-font/vector visual tokens. No bundled brand assets or external fonts are loaded.
//
// The dictionary is merged into the LoginWindow resources (so every upstream
// FindStyle lookup — saved-account picker, notice, editor — gets the same
// treatment) and into the card itself (so the shared brushes win over the
// upstream clock theme, which writes straight into Window.Resources).
using System;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Markup;
using System.Windows.Media;

namespace Nte.LoginHost.Presentation;

internal static class LoginVisuals
{
    internal const string Header = "Header";
    internal const string Body = "Body";
    internal const string Status = "Status";
    internal const string InputBorder = "InputBorder";
    internal const string Text = "Text";
    internal const string MutedText = "MutedText";
    internal const string Bezel = "Bezel";
    internal const string GhostButton = "GhostButton";
    internal const string PrimaryButton = "PrimaryButton";
    internal const string SecondaryButton = "SecondaryButton";
    internal const string LinkButton = "LinkButton";
    internal const string CheckBox = "FormCheckBoxStyle";
    internal const string TextField = "LoginTextBoxStyle";
    internal const string PasswordField = "LoginPasswordBoxStyle";
    internal const string Scroll = "LoginScrollViewerStyle";

    internal static readonly FontFamily Font = new("Microsoft YaHei UI, Segoe UI");
    internal static readonly FontFamily Wordmark = new("Bahnschrift SemiBold, Segoe UI Semibold, Segoe UI");

    private static readonly Lazy<ResourceDictionary> Shared = new(Create);

    private static bool pillsRegistered;

    internal static ResourceDictionary Create()
    {
        RegisterPills();
        return (ResourceDictionary)XamlReader.Parse(Xaml);
    }

    // WPF draws a Border whose CornerRadius exceeds half its height as an
    // ellipse, so template borders tagged "pill" take exactly half their
    // arranged height instead of a fixed radius.
    private static void RegisterPills()
    {
        if (pillsRegistered) return;
        pillsRegistered = true;
        EventManager.RegisterClassHandler(typeof(Border), FrameworkElement.SizeChangedEvent,
            new SizeChangedEventHandler((sender, args) =>
            {
                if (sender is Border { Tag: "pill" } pill && args.HeightChanged)
                    pill.CornerRadius = new CornerRadius(Math.Max(0, pill.ActualHeight / 2));
            }));
    }

    internal static Brush Brush(string key) => key switch
    {
        Header => Solid(0x1E, 0x1F, 0x22),
        Body => Solid(0xE3, 0xE3, 0xE3),
        Status => Solid(0x2B, 0x2C, 0x30),
        InputBorder => Solid(0xD0, 0xD1, 0xD3),
        Text => Solid(0x2A, 0x2B, 0x2E),
        MutedText => Solid(0x8A, 0x8C, 0x90),
        Bezel => Solid(0x3A, 0x3B, 0x3F),
        _ => Brushes.Transparent
    };

    internal static Style Style(string key) => (Style)Shared.Value[key];

    private static SolidColorBrush Solid(byte r, byte g, byte b, byte a = 0xFF)
    {
        SolidColorBrush brush = new(Color.FromArgb(a, r, g, b));
        brush.Freeze();
        return brush;
    }

    // Upstream keys (AccentButtonStyle, SmsButtonStyle, QuietButtonStyle,
    // LoginTextBoxStyle, FormCheckBoxStyle, SavedAccountListStyle, ...) are
    // re-declared here so the whole window speaks one visual language.
    private const string Xaml = """
<ResourceDictionary xmlns="http://schemas.microsoft.com/winfx/2006/xaml/presentation"
                    xmlns:x="http://schemas.microsoft.com/winfx/2006/xaml">
  <SolidColorBrush x:Key="TextBrush" Color="#2A2B2E" />
  <SolidColorBrush x:Key="MutedTextBrush" Color="#7C7E83" />
  <SolidColorBrush x:Key="AccentBrush" Color="#2A2B2E" />
  <SolidColorBrush x:Key="InputBrush" Color="#FFFFFF" />
  <SolidColorBrush x:Key="InputBorderBrush" Color="#D0D1D3" />
  <SolidColorBrush x:Key="GlobalPopupBrush" Color="#EEEEEE" />
  <FontFamily x:Key="LoginFont">Microsoft YaHei UI, Segoe UI</FontFamily>

  <Style x:Key="PrimaryButton" TargetType="Button">
    <Setter Property="FontFamily" Value="{StaticResource LoginFont}" />
    <Setter Property="FontSize" Value="16" />
    <Setter Property="FontWeight" Value="Bold" />
    <Setter Property="Foreground" Value="#1F2023" />
    <Setter Property="MinHeight" Value="40" />
    <Setter Property="Padding" Value="16,4" />
    <Setter Property="Cursor" Value="Hand" />
    <Setter Property="FocusVisualStyle" Value="{x:Null}" />
    <Setter Property="HorizontalContentAlignment" Value="Center" />
    <Setter Property="VerticalContentAlignment" Value="Center" />
    <Setter Property="Template">
      <Setter.Value>
        <ControlTemplate TargetType="Button">
          <Border x:Name="Ring" CornerRadius="100" Tag="pill" Background="#F6F6F6" BorderBrush="#D3D4D6" BorderThickness="1" Padding="3">
            <Border x:Name="Surface" CornerRadius="100" Tag="pill" BorderBrush="#8E9094" BorderThickness="1.2">
              <Border.Background>
                <LinearGradientBrush StartPoint="0,0" EndPoint="0,1">
                  <GradientStop Color="#FDFDFD" Offset="0" />
                  <GradientStop Color="#EDEDED" Offset="1" />
                </LinearGradientBrush>
              </Border.Background>
              <ContentPresenter Margin="{TemplateBinding Padding}" RecognizesAccessKey="True"
                                HorizontalAlignment="{TemplateBinding HorizontalContentAlignment}"
                                VerticalAlignment="{TemplateBinding VerticalContentAlignment}" />
            </Border>
          </Border>
          <ControlTemplate.Triggers>
            <Trigger Property="IsMouseOver" Value="True">
              <Setter TargetName="Surface" Property="Background" Value="#FFFFFF" />
              <Setter TargetName="Surface" Property="BorderBrush" Value="#55575C" />
            </Trigger>
            <Trigger Property="IsPressed" Value="True">
              <Setter TargetName="Surface" Property="Background" Value="#E2E2E2" />
            </Trigger>
            <Trigger Property="IsKeyboardFocused" Value="True">
              <Setter TargetName="Surface" Property="BorderBrush" Value="#1F2023" />
              <Setter TargetName="Surface" Property="BorderThickness" Value="2" />
            </Trigger>
            <Trigger Property="IsEnabled" Value="False">
              <Setter TargetName="Ring" Property="Background" Value="Transparent" />
              <Setter TargetName="Ring" Property="BorderBrush" Value="Transparent" />
              <Setter TargetName="Surface" Property="Background" Value="#D3D3D4" />
              <Setter TargetName="Surface" Property="BorderBrush" Value="#D3D3D4" />
              <Setter Property="Foreground" Value="#8B8C90" />
            </Trigger>
          </ControlTemplate.Triggers>
        </ControlTemplate>
      </Setter.Value>
    </Setter>
  </Style>

  <Style x:Key="SecondaryButton" TargetType="Button">
    <Setter Property="FontFamily" Value="{StaticResource LoginFont}" />
    <Setter Property="FontSize" Value="14" />
    <Setter Property="FontWeight" Value="Bold" />
    <Setter Property="Foreground" Value="#46484C" />
    <Setter Property="MinHeight" Value="32" />
    <Setter Property="Padding" Value="14,4" />
    <Setter Property="Cursor" Value="Hand" />
    <Setter Property="FocusVisualStyle" Value="{x:Null}" />
    <Setter Property="HorizontalContentAlignment" Value="Center" />
    <Setter Property="VerticalContentAlignment" Value="Center" />
    <Setter Property="Template">
      <Setter.Value>
        <ControlTemplate TargetType="Button">
          <Border x:Name="Surface" CornerRadius="100" Tag="pill" Background="#D4D4D5" BorderBrush="Transparent" BorderThickness="2">
            <ContentPresenter Margin="{TemplateBinding Padding}" RecognizesAccessKey="True"
                              HorizontalAlignment="{TemplateBinding HorizontalContentAlignment}"
                              VerticalAlignment="{TemplateBinding VerticalContentAlignment}" />
          </Border>
          <ControlTemplate.Triggers>
            <Trigger Property="IsMouseOver" Value="True">
              <Setter TargetName="Surface" Property="Background" Value="#CACACC" />
              <Setter Property="Foreground" Value="#2A2B2E" />
            </Trigger>
            <Trigger Property="IsPressed" Value="True">
              <Setter TargetName="Surface" Property="Background" Value="#BEBFC1" />
            </Trigger>
            <Trigger Property="IsKeyboardFocused" Value="True">
              <Setter TargetName="Surface" Property="BorderBrush" Value="#1F2023" />
            </Trigger>
            <Trigger Property="IsEnabled" Value="False">
              <Setter TargetName="Surface" Property="Opacity" Value="0.55" />
            </Trigger>
          </ControlTemplate.Triggers>
        </ControlTemplate>
      </Setter.Value>
    </Setter>
  </Style>

  <Style x:Key="LinkButton" TargetType="Button">
    <Setter Property="FontFamily" Value="{StaticResource LoginFont}" />
    <Setter Property="FontSize" Value="13" />
    <Setter Property="FontWeight" Value="Bold" />
    <Setter Property="Foreground" Value="#3A3B3F" />
    <Setter Property="Padding" Value="8,2" />
    <Setter Property="Cursor" Value="Hand" />
    <Setter Property="FocusVisualStyle" Value="{x:Null}" />
    <Setter Property="HorizontalContentAlignment" Value="Center" />
    <Setter Property="VerticalContentAlignment" Value="Center" />
    <Setter Property="Template">
      <Setter.Value>
        <ControlTemplate TargetType="Button">
          <Border x:Name="Surface" CornerRadius="100" Tag="pill" Background="Transparent" BorderBrush="Transparent" BorderThickness="1.5">
            <ContentPresenter Margin="{TemplateBinding Padding}" RecognizesAccessKey="True"
                              HorizontalAlignment="{TemplateBinding HorizontalContentAlignment}"
                              VerticalAlignment="{TemplateBinding VerticalContentAlignment}" />
          </Border>
          <ControlTemplate.Triggers>
            <Trigger Property="IsMouseOver" Value="True">
              <Setter Property="Foreground" Value="#000000" />
              <Setter TargetName="Surface" Property="Background" Value="#0F000000" />
            </Trigger>
            <Trigger Property="IsKeyboardFocused" Value="True">
              <Setter TargetName="Surface" Property="BorderBrush" Value="#1F2023" />
            </Trigger>
            <Trigger Property="IsEnabled" Value="False">
              <Setter Property="Foreground" Value="#75777B" />
            </Trigger>
          </ControlTemplate.Triggers>
        </ControlTemplate>
      </Setter.Value>
    </Setter>
  </Style>

  <Style x:Key="GhostButton" TargetType="Button">
    <Setter Property="Foreground" Value="#FFFFFF" />
    <Setter Property="FontFamily" Value="{StaticResource LoginFont}" />
    <Setter Property="FontSize" Value="13" />
    <Setter Property="Padding" Value="0" />
    <Setter Property="Cursor" Value="Hand" />
    <Setter Property="FocusVisualStyle" Value="{x:Null}" />
    <Setter Property="HorizontalContentAlignment" Value="Center" />
    <Setter Property="VerticalContentAlignment" Value="Center" />
    <Setter Property="Template">
      <Setter.Value>
        <ControlTemplate TargetType="Button">
          <Border x:Name="Surface" CornerRadius="100" Tag="pill" Background="Transparent" BorderBrush="Transparent" BorderThickness="1.5">
            <ContentPresenter Margin="{TemplateBinding Padding}"
                              HorizontalAlignment="{TemplateBinding HorizontalContentAlignment}"
                              VerticalAlignment="{TemplateBinding VerticalContentAlignment}" />
          </Border>
          <ControlTemplate.Triggers>
            <Trigger Property="IsMouseOver" Value="True">
              <Setter TargetName="Surface" Property="Background" Value="#1FFFFFFF" />
            </Trigger>
            <Trigger Property="IsPressed" Value="True">
              <Setter TargetName="Surface" Property="Background" Value="#33FFFFFF" />
            </Trigger>
            <Trigger Property="IsKeyboardFocused" Value="True">
              <Setter TargetName="Surface" Property="BorderBrush" Value="#CCFFFFFF" />
            </Trigger>
            <Trigger Property="IsEnabled" Value="False">
              <Setter TargetName="Surface" Property="Opacity" Value="0.4" />
            </Trigger>
          </ControlTemplate.Triggers>
        </ControlTemplate>
      </Setter.Value>
    </Setter>
  </Style>

  <Style x:Key="AccentButtonStyle" TargetType="Button" BasedOn="{StaticResource PrimaryButton}" />
  <Style x:Key="ProviderButtonStyle" TargetType="Button" BasedOn="{StaticResource SecondaryButton}" />
  <Style x:Key="SmsButtonStyle" TargetType="Button" BasedOn="{StaticResource SecondaryButton}" />
  <Style x:Key="AcrylicButtonStyle" TargetType="Button" BasedOn="{StaticResource SecondaryButton}" />
  <Style x:Key="QuietButtonStyle" TargetType="Button" BasedOn="{StaticResource LinkButton}">
    <Setter Property="FontSize" Value="12" />
  </Style>

  <Style x:Key="LoginTextBoxStyle" TargetType="TextBox">
    <Setter Property="FontFamily" Value="{StaticResource LoginFont}" />
    <Setter Property="FontSize" Value="14" />
    <Setter Property="Foreground" Value="{DynamicResource TextBrush}" />
    <Setter Property="Background" Value="{DynamicResource InputBrush}" />
    <Setter Property="BorderBrush" Value="{DynamicResource InputBorderBrush}" />
    <Setter Property="BorderThickness" Value="1" />
    <Setter Property="CaretBrush" Value="#2A2B2E" />
    <Setter Property="SelectionBrush" Value="#8E9094" />
    <Setter Property="Padding" Value="16,0" />
    <Setter Property="MinHeight" Value="40" />
    <Setter Property="VerticalContentAlignment" Value="Center" />
    <Setter Property="FocusVisualStyle" Value="{x:Null}" />
    <Setter Property="Template">
      <Setter.Value>
        <ControlTemplate TargetType="TextBox">
          <Border x:Name="Shell" CornerRadius="100" Tag="pill" Background="{TemplateBinding Background}"
                  BorderBrush="{TemplateBinding BorderBrush}" BorderThickness="{TemplateBinding BorderThickness}">
            <ScrollViewer x:Name="PART_ContentHost" Margin="{TemplateBinding Padding}" Focusable="False"
                          VerticalAlignment="{TemplateBinding VerticalContentAlignment}"
                          HorizontalScrollBarVisibility="Hidden" VerticalScrollBarVisibility="Hidden" />
          </Border>
          <ControlTemplate.Triggers>
            <Trigger Property="IsKeyboardFocused" Value="True">
              <Setter TargetName="Shell" Property="BorderBrush" Value="#55575C" />
            </Trigger>
            <Trigger Property="IsEnabled" Value="False">
              <Setter TargetName="Shell" Property="Opacity" Value="0.55" />
            </Trigger>
          </ControlTemplate.Triggers>
        </ControlTemplate>
      </Setter.Value>
    </Setter>
  </Style>

  <Style x:Key="LoginPasswordBoxStyle" TargetType="PasswordBox">
    <Setter Property="FontFamily" Value="{StaticResource LoginFont}" />
    <Setter Property="FontSize" Value="14" />
    <Setter Property="Foreground" Value="{DynamicResource TextBrush}" />
    <Setter Property="Background" Value="{DynamicResource InputBrush}" />
    <Setter Property="BorderBrush" Value="{DynamicResource InputBorderBrush}" />
    <Setter Property="BorderThickness" Value="1" />
    <Setter Property="CaretBrush" Value="#2A2B2E" />
    <Setter Property="Padding" Value="16,0" />
    <Setter Property="MinHeight" Value="40" />
    <Setter Property="VerticalContentAlignment" Value="Center" />
    <Setter Property="FocusVisualStyle" Value="{x:Null}" />
    <Setter Property="Template">
      <Setter.Value>
        <ControlTemplate TargetType="PasswordBox">
          <Border x:Name="Shell" CornerRadius="100" Tag="pill" Background="{TemplateBinding Background}"
                  BorderBrush="{TemplateBinding BorderBrush}" BorderThickness="{TemplateBinding BorderThickness}">
            <ScrollViewer x:Name="PART_ContentHost" Margin="{TemplateBinding Padding}" Focusable="False"
                          VerticalAlignment="{TemplateBinding VerticalContentAlignment}"
                          HorizontalScrollBarVisibility="Hidden" VerticalScrollBarVisibility="Hidden" />
          </Border>
          <ControlTemplate.Triggers>
            <Trigger Property="IsKeyboardFocused" Value="True">
              <Setter TargetName="Shell" Property="BorderBrush" Value="#55575C" />
            </Trigger>
            <Trigger Property="IsEnabled" Value="False">
              <Setter TargetName="Shell" Property="Opacity" Value="0.55" />
            </Trigger>
          </ControlTemplate.Triggers>
        </ControlTemplate>
      </Setter.Value>
    </Setter>
  </Style>

  <Style x:Key="FormCheckBoxStyle" TargetType="CheckBox">
    <Setter Property="FontFamily" Value="{StaticResource LoginFont}" />
    <Setter Property="FontSize" Value="12" />
    <Setter Property="FontWeight" Value="SemiBold" />
    <Setter Property="Foreground" Value="#5F6165" />
    <Setter Property="MinHeight" Value="24" />
    <Setter Property="Cursor" Value="Hand" />
    <Setter Property="FocusVisualStyle" Value="{x:Null}" />
    <Setter Property="Template">
      <Setter.Value>
        <ControlTemplate TargetType="CheckBox">
          <Grid Background="Transparent">
            <Grid.ColumnDefinitions>
              <ColumnDefinition Width="20" />
              <ColumnDefinition Width="*" />
            </Grid.ColumnDefinitions>
            <Grid Width="18" Height="18" VerticalAlignment="Center">
              <Ellipse x:Name="Ring" Fill="#FFFFFF" Stroke="#9C9EA2" StrokeThickness="1.6" />
              <Ellipse x:Name="Dot" Fill="#2A2B2E" Visibility="Collapsed" />
              <Path x:Name="Check" Data="M 5,9.2 L 7.8,12 L 13,6.6" Stroke="#FFFFFF" StrokeThickness="1.8"
                    StrokeStartLineCap="Round" StrokeEndLineCap="Round" StrokeLineJoin="Round" Visibility="Collapsed" />
            </Grid>
            <ContentPresenter Grid.Column="1" Margin="8,0,0,0" VerticalAlignment="Center" RecognizesAccessKey="True" />
          </Grid>
          <ControlTemplate.Triggers>
            <Trigger Property="IsMouseOver" Value="True">
              <Setter TargetName="Ring" Property="Stroke" Value="#55575C" />
            </Trigger>
            <Trigger Property="IsKeyboardFocused" Value="True">
              <Setter TargetName="Ring" Property="Stroke" Value="#1F2023" />
              <Setter TargetName="Ring" Property="StrokeThickness" Value="2.2" />
            </Trigger>
            <Trigger Property="IsChecked" Value="True">
              <Setter TargetName="Dot" Property="Visibility" Value="Visible" />
              <Setter TargetName="Check" Property="Visibility" Value="Visible" />
            </Trigger>
            <Trigger Property="IsEnabled" Value="False">
              <Setter Property="Opacity" Value="0.5" />
            </Trigger>
          </ControlTemplate.Triggers>
        </ControlTemplate>
      </Setter.Value>
    </Setter>
  </Style>

  <Style x:Key="SlimScrollBar" TargetType="ScrollBar">
    <Setter Property="Width" Value="6" />
    <Setter Property="MinWidth" Value="6" />
    <Setter Property="Template">
      <Setter.Value>
        <ControlTemplate TargetType="ScrollBar">
          <Track x:Name="PART_Track" IsDirectionReversed="True">
            <Track.Thumb>
              <Thumb>
                <Thumb.Template>
                  <ControlTemplate TargetType="Thumb">
                    <Border CornerRadius="3" Background="#A2A4A8" />
                  </ControlTemplate>
                </Thumb.Template>
              </Thumb>
            </Track.Thumb>
          </Track>
        </ControlTemplate>
      </Setter.Value>
    </Setter>
  </Style>

  <Style x:Key="LoginScrollViewerStyle" TargetType="ScrollViewer">
    <Setter Property="Template">
      <Setter.Value>
        <ControlTemplate TargetType="ScrollViewer">
          <Grid>
            <Grid.ColumnDefinitions>
              <ColumnDefinition Width="*" />
              <ColumnDefinition Width="Auto" />
            </Grid.ColumnDefinitions>
            <ScrollContentPresenter x:Name="PART_ScrollContentPresenter" Margin="{TemplateBinding Padding}"
                                    CanContentScroll="{TemplateBinding CanContentScroll}" />
            <ScrollBar x:Name="PART_VerticalScrollBar" Grid.Column="1" Margin="4,2,0,2"
                       Style="{StaticResource SlimScrollBar}"
                       Value="{TemplateBinding VerticalOffset}" Maximum="{TemplateBinding ScrollableHeight}"
                       ViewportSize="{TemplateBinding ViewportHeight}"
                       Visibility="{TemplateBinding ComputedVerticalScrollBarVisibility}" />
          </Grid>
        </ControlTemplate>
      </Setter.Value>
    </Setter>
  </Style>

  <Style x:Key="OddwardAccountItem" TargetType="ListBoxItem">
    <Setter Property="HorizontalContentAlignment" Value="Stretch" />
    <Setter Property="Foreground" Value="#FFFFFF" />
    <Setter Property="Margin" Value="0,0,0,8" />
    <Setter Property="Cursor" Value="Hand" />
    <Setter Property="FocusVisualStyle" Value="{x:Null}" />
    <Setter Property="AutomationProperties.Name" Value="{Binding AccessibilityLabel}" />
    <Setter Property="Template">
      <Setter.Value>
        <ControlTemplate TargetType="ListBoxItem">
          <Border x:Name="Surface" CornerRadius="18" Background="#26272B" BorderBrush="#26272B"
                  BorderThickness="2" Padding="14,11" MinHeight="70">
            <ContentPresenter VerticalAlignment="Center" />
          </Border>
          <ControlTemplate.Triggers>
            <Trigger Property="IsMouseOver" Value="True">
              <Setter TargetName="Surface" Property="Background" Value="#34353A" />
              <Setter TargetName="Surface" Property="BorderBrush" Value="#34353A" />
            </Trigger>
            <Trigger Property="IsSelected" Value="True">
              <Setter TargetName="Surface" Property="Background" Value="#FFFFFF" />
              <Setter TargetName="Surface" Property="BorderBrush" Value="#FFFFFF" />
              <Setter Property="Foreground" Value="#4A4C50" />
            </Trigger>
            <Trigger Property="IsKeyboardFocused" Value="True">
              <Setter TargetName="Surface" Property="BorderBrush" Value="#8E9094" />
            </Trigger>
            <Trigger Property="IsEnabled" Value="False">
              <Setter Property="Opacity" Value="0.55" />
            </Trigger>
          </ControlTemplate.Triggers>
        </ControlTemplate>
      </Setter.Value>
    </Setter>
  </Style>

  <DataTemplate x:Key="OddwardAccountTemplate">
    <Grid ToolTip="{Binding AccessibilityLabel}">
      <Grid.ColumnDefinitions>
        <ColumnDefinition Width="44" />
        <ColumnDefinition Width="Auto" />
        <ColumnDefinition Width="*" />
        <ColumnDefinition Width="Auto" />
      </Grid.ColumnDefinitions>
      <Grid Width="44" Height="44" VerticalAlignment="Center">
        <Ellipse Fill="#3B3C41" />
        <Viewbox Width="20" Height="20">
          <Path Fill="#FFFFFF" Data="M12,12 A4.6,4.6 0 1 0 12,2.8 A4.6,4.6 0 1 0 12,12 Z M3.5,22 C3.5,17.2 7.3,14.2 12,14.2 C16.7,14.2 20.5,17.2 20.5,22 Z" />
        </Viewbox>
      </Grid>
      <Rectangle Grid.Column="1" Width="2" Height="38" Margin="14,0" RadiusX="1" RadiusY="1"
                 Fill="{Binding Foreground, RelativeSource={RelativeSource AncestorType=ListBoxItem}}" Opacity="0.3" />
      <StackPanel Grid.Column="2" VerticalAlignment="Center">
        <TextBlock Text="{Binding Primary}" FontSize="16" FontWeight="Bold" TextTrimming="CharacterEllipsis" />
        <TextBlock Text="{Binding Secondary}" FontSize="12.5" FontWeight="SemiBold" Opacity="0.8" Margin="0,3,0,0"
                   TextTrimming="CharacterEllipsis" FlowDirection="LeftToRight" />
      </StackPanel>
      <TextBlock Grid.Column="3" Text="{Binding Source}" FontSize="11" Opacity="0.7" Margin="8,0,0,0"
                 VerticalAlignment="Center" />
    </Grid>
  </DataTemplate>

  <Style x:Key="SavedAccountListStyle" TargetType="ListBox">
    <Setter Property="Background" Value="Transparent" />
    <Setter Property="BorderThickness" Value="0" />
    <Setter Property="Padding" Value="0" />
    <Setter Property="FontFamily" Value="{StaticResource LoginFont}" />
    <Setter Property="ItemContainerStyle" Value="{StaticResource OddwardAccountItem}" />
    <Setter Property="ItemTemplate" Value="{StaticResource OddwardAccountTemplate}" />
    <Setter Property="ScrollViewer.HorizontalScrollBarVisibility" Value="Disabled" />
    <Setter Property="ScrollViewer.VerticalScrollBarVisibility" Value="Auto" />
    <Setter Property="Template">
      <Setter.Value>
        <ControlTemplate TargetType="ListBox">
          <ScrollViewer Focusable="False" Style="{StaticResource LoginScrollViewerStyle}" CanContentScroll="True">
            <ItemsPresenter />
          </ScrollViewer>
        </ControlTemplate>
      </Setter.Value>
    </Setter>
  </Style>
</ResourceDictionary>
""";
}
